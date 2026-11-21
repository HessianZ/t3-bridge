package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_runtime_meetings.html
var configRuntimeMeetingsHTML string

type runtimeMeetingTarget runtimeSettingsTarget

func (t runtimeMeetingTarget) URL() string {
	return "/runtime/meetings?" + url.Values{
		"process": {t.Process}, "tenant": {t.Account.Tenant}, "appId": {t.Account.AppID},
	}.Encode()
}

type runtimeMeetingReview struct {
	Request bridge.LocalMeetingRequest
	Token   string
	target  runtimeMeetingTarget
	expires time.Time
}

type runtimeMeetingReviews struct {
	mu      sync.Mutex
	pending runtimeMeetingReview
}

func configRuntimeMeetingsHandler(list func() ([]receiverProcess, error), operate liveMeetingOperation) http.HandlerFunc {
	page := template.Must(newWorkspacePage("runtime meetings").Funcs(template.FuncMap{
		"phaseText":  meetingPhaseText,
		"originText": meetingOriginText,
	}).Parse(configRuntimeMeetingsHTML))
	var reviews runtimeMeetingReviews
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}

		post := r.Method == http.MethodPost
		request := bridge.LocalMeetingRequest{Action: "status"}
		var values url.Values
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 1024)
			if r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "会议操作表单无效。", http.StatusBadRequest)
				return
			}
			values = r.PostForm
			var valid bool
			request, valid = readRuntimeMeetingAction(values)
			if !valid {
				http.Error(w, "请选择有效的会议操作并填写完整信息。", http.StatusBadRequest)
				return
			}
		} else {
			var err error
			values, err = url.ParseQuery(r.URL.RawQuery)
			if err != nil || (len(values) != 0 && len(values) != 3) {
				http.Error(w, "请选择完整的接收进程、租户和 App。", http.StatusBadRequest)
				return
			}
		}
		if (post || len(values) != 0) && !validRuntimeReconnectTarget(values) {
			http.Error(w, "请选择完整的接收进程、租户和 App。", http.StatusBadRequest)
			return
		}
		target := runtimeMeetingTarget{Process: values.Get("process"), Account: receiverProcessAccount{
			Tenant: values.Get("tenant"), AppID: values.Get("appId"),
		}}
		var view struct {
			Targets    []runtimeMeetingTarget
			Selected   *runtimeMeetingTarget
			Meetings   *bridge.LocalMeetingView
			Review     *runtimeMeetingReview
			Post       bool
			Notice     string
			RefreshURL string
		}
		view.Post, view.RefreshURL = post, "/runtime/meetings"
		if target.Process != "" {
			view.RefreshURL = target.URL()
		}
		processes, listErr := list()
		if listErr == nil {
			for _, process := range processes {
				if !process.Meetings {
					continue
				}
				for _, account := range process.Accounts {
					candidate := runtimeMeetingTarget{Process: process.ID, Account: account, PID: process.PID}
					if target.Process == "" {
						view.Targets = append(view.Targets, candidate)
					} else if candidate.Process == target.Process && candidate.Account == target.Account {
						view.Selected = &candidate
					}
				}
			}
		}
		confirming := post && values.Get("confirm") == "yes"
		confirmed := confirming && reviews.take(target, request, values.Get("review"))
		status := http.StatusOK
		switch {
		case listErr != nil:
			status, view.Notice = http.StatusServiceUnavailable, "接收进程清单暂不可用，请刷新状态。"
		case target.Process != "" && view.Selected == nil:
			status, view.Notice = http.StatusNotFound, "该接收进程或 App 已不可用，请返回运行状态重新选择。"
		case view.Selected == nil:
		case post && !confirming:
			review := runtimeMeetingReview{Request: request, Token: rand.Text(), target: target, expires: time.Now().Add(5 * time.Minute)}
			reviews.mu.Lock()
			reviews.pending = review
			reviews.mu.Unlock()
			view.Review = &review
		case confirming && !confirmed:
			status, view.Notice = http.StatusConflict, "会议操作复核已失效，请刷新状态后重新复核。"
		default:
			// Leave time to render an unknown-result page after the receiver deadline.
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(liveMeetingTimeout + configWebWriteTimeout))
			ctx, cancel := context.WithTimeout(r.Context(), liveMeetingTimeout)
			meetings, err := operate(ctx, target.Process, target.Account.Tenant, target.Account.AppID, request)
			cancel()
			switch {
			case err != nil && post:
				status, view.Notice = http.StatusBadGateway, "会议操作结果未确认。请刷新状态，并在飞书核对参会名单和等候室。"
			case err != nil:
				status, view.Notice = http.StatusBadGateway, "暂时无法读取会议状态，请刷新状态。"
			default:
				view.Meetings = &meetings
				view.Notice = meetingOutcomeText(meetings.Outcome)
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func readRuntimeMeetingAction(values url.Values) (bridge.LocalMeetingRequest, bool) {
	request := bridge.LocalMeetingRequest{Action: values.Get("action"), Number: values.Get("number"), Revision: values.Get("revision")}
	keys := []string{"process", "tenant", "appId", "action", "number"}
	switch request.Action {
	case "join":
	case "leave", "confirm-left":
		keys = append(keys, "revision")
	default:
		return request, false
	}
	if _, confirming := values["confirm"]; confirming {
		if values.Get("confirm") != "yes" || values.Get("review") == "" {
			return request, false
		}
		keys = append(keys, "confirm", "review")
	}
	if len(values) != len(keys) {
		return request, false
	}
	for _, key := range keys {
		if len(values[key]) != 1 {
			return request, false
		}
	}
	return request, request.Validate() == nil
}

func (reviews *runtimeMeetingReviews) take(target runtimeMeetingTarget, request bridge.LocalMeetingRequest, token string) bool {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	if reviews.pending.Token != token {
		return false
	}
	review := reviews.pending
	reviews.pending = runtimeMeetingReview{}
	return time.Now().Before(review.expires) && review.target == target && review.Request == request
}
