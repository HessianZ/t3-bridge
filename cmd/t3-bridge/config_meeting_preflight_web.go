package main

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_meeting_preflight_web.html
var configMeetingPreflightHTML string

type configMeetingPreflightPage struct {
	Sources     []webSource
	Source      webSource
	Choices     []meetingPermissionChoice
	ChoiceIndex int
	Report      *bridge.MeetingPermissionReport
	Error       string
}

func configMeetingPreflightHandler(sources func() ([]webSource, error),
	check, checkAvailability func(context.Context, webSource, string, string) (bridge.MeetingPermissionReport, error),
) http.HandlerFunc {
	page := template.Must(newWorkspacePage("meeting preflight").Funcs(template.FuncMap{
		"stateText":            meetingPermissionStateText,
		"scopeText":            meetingPermissionScopeText,
		"publicationStateText": meetingPublicationStateText,
		"eventStateText":       meetingEventStateText,
		"availabilityText":     meetingAvailabilityText,
	}).Parse(configMeetingPreflightHTML))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		post := r.Method == http.MethodPost
		if !post && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !runtimeRequestOriginAllowed(r) || (!post &&
			(len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1)) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		// Native same-origin form POSTs need a non-opaque Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		var id, revision string
		var choice int
		var probeAvailability bool
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 2048)
			if r.URL.ForceQuery || r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "会议权限预检表单无效。", http.StatusBadRequest)
				return
			}
			probe := r.PostForm["probe"]
			probeAvailability = len(probe) == 1 && probe[0] == "availability"
			if (len(r.PostForm) != 3 && !(len(r.PostForm) == 4 && probeAvailability)) ||
				len(r.PostForm["source"]) != 1 || len(r.PostForm["revision"]) != 1 || len(r.PostForm["choice"]) != 1 {
				http.Error(w, "会议权限预检表单无效。", http.StatusBadRequest)
				return
			}
			id, revision = r.PostForm.Get("source"), r.PostForm.Get("revision")
			value := r.PostForm.Get("choice")
			var err error
			choice, err = strconv.Atoi(value)
			if id == "" || revision == "" || err != nil || choice < 0 || strconv.Itoa(choice) != value {
				http.Error(w, "请选择已保存的配置和要检查的应用。", http.StatusBadRequest)
				return
			}
		} else {
			query, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil || (len(query) != 0 && (len(query) != 1 || len(query["source"]) != 1 || query.Get("source") == "")) {
				http.Error(w, "请选择已保存的配置。", http.StatusBadRequest)
				return
			}
			id = query.Get("source")
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置。", http.StatusConflict)
			return
		}
		view := configMeetingPreflightPage{ChoiceIndex: -1}
		for _, source := range available {
			if source.Path == "" {
				continue
			}
			current, choices, err := savedMeetingPermissionChoices(source.Path)
			if err != nil {
				continue
			}
			source.Revision = current
			view.Sources = append(view.Sources, source)
			if id == "" {
				id = source.ID
			}
			if source.ID == id {
				view.Source, view.Choices = source, choices
			}
		}

		status := http.StatusOK
		switch {
		case id != "" && view.Source.ID == "", post && revision != view.Source.Revision:
			status, view.Error = http.StatusConflict, errMeetingPermissionSourceChanged.Error()
			view.Choices = nil
		case post && choice >= len(view.Choices):
			status, view.Error = http.StatusBadRequest, "请选择已保存配置中的应用。"
		case post:
			selected := view.Choices[choice]
			view.ChoiceIndex = choice
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(meetingPermissionTimeout + configWebWriteTimeout))
			runCheck := check
			if probeAvailability {
				runCheck = checkAvailability
			}
			report, err := runCheck(r.Context(), view.Source, selected.AppID, selected.Tenant)
			switch {
			case errors.Is(err, errMeetingPermissionSourceChanged):
				status, view.Error = http.StatusConflict, errMeetingPermissionSourceChanged.Error()
				view.Choices = nil
			case err != nil:
				status, view.Error = http.StatusUnprocessableEntity, "无法读取会议权限。请核对应用凭据和飞书连接后重试。"
			default:
				view.Report = &report
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}
