package main

import (
	"context"
	_ "embed"
	"net/http"
	"net/url"
	"slices"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_runtime_reconnect.html
var configRuntimeReconnectHTML string

type runtimeReconnectTarget struct {
	Process       string
	Account       receiverProcessAccount
	PID           int
	ReconnectWait bool
}

func (t runtimeReconnectTarget) URL() string {
	return "/runtime/reconnect?" + url.Values{
		"process": {t.Process}, "tenant": {t.Account.Tenant}, "appId": {t.Account.AppID},
	}.Encode()
}

func configRuntimeReconnectHandler(list func() ([]receiverProcess, error), reconnect liveReconnectOperation) http.HandlerFunc {
	page := newWorkspacePage("runtime reconnect").Parse(configRuntimeReconnectHTML)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Referrer-Policy", "same-origin")

		post := r.Method == http.MethodPost
		values, err := url.ParseQuery(r.URL.RawQuery)
		fieldCount := 3
		action := bridge.ReconnectNow
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 1024)
			if r.URL.RawQuery != "" || r.ParseForm() != nil || len(r.PostForm["confirm"]) != 1 || r.PostForm.Get("confirm") != "reconnect" {
				http.Error(w, "需要明确确认重连。", http.StatusBadRequest)
				return
			}
			values = r.PostForm
			fieldCount++
			if actions, present := values["action"]; present {
				fieldCount++
				if len(actions) != 1 {
					http.Error(w, "请选择一个重连操作。", http.StatusBadRequest)
					return
				}
				action = bridge.ReconnectAction(actions[0])
				if !action.Valid() {
					http.Error(w, "请选择一个有效的重连操作。", http.StatusBadRequest)
					return
				}
			}
		}
		if err != nil || ((post || len(values) != 0) && (len(values) != fieldCount || !validRuntimeReconnectTarget(values))) {
			http.Error(w, "请选择完整的接收进程、租户和 App。", http.StatusBadRequest)
			return
		}

		var view struct {
			Targets  []runtimeReconnectTarget
			Selected *runtimeReconnectTarget
			Post     bool
			Notice   string
		}
		view.Post = post
		processes, err := list()
		status := http.StatusOK
		if err != nil {
			status, view.Notice = http.StatusServiceUnavailable, "接收进程清单暂不可用。"
		} else {
			for _, process := range processes {
				if !process.Reconnect {
					continue
				}
				for _, account := range process.Accounts {
					view.Targets = append(view.Targets, runtimeReconnectTarget{
						Process: process.ID, Account: account, PID: process.PID, ReconnectWait: process.ReconnectWait,
					})
				}
			}
			if len(values) != 0 {
				selected := slices.IndexFunc(view.Targets, func(target runtimeReconnectTarget) bool {
					return target.Process == values.Get("process") && target.Account.Tenant == values.Get("tenant") && target.Account.AppID == values.Get("appId")
				})
				if selected < 0 {
					status, view.Notice = http.StatusNotFound, "该接收进程或 App 已不可用，请返回运行状态重新选择。"
				} else {
					view.Selected = &view.Targets[selected]
					if post {
						if action != bridge.ReconnectNow && !view.Selected.ReconnectWait {
							status, view.Notice = http.StatusBadRequest, "此接收进程仅支持立即重连。"
						} else {
							status, view.Notice = reconnectRuntimeApp(r.Context(), reconnect, *view.Selected, action)
						}
					}
				}
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}

func validRuntimeReconnectTarget(values url.Values) bool {
	return len(values["process"]) == 1 && receiverProcessID.MatchString(values.Get("process")) &&
		len(values["tenant"]) == 1 && (values.Get("tenant") == "feishu" || values.Get("tenant") == "lark") &&
		len(values["appId"]) == 1 && values.Get("appId") != ""
}

func reconnectRuntimeApp(ctx context.Context, reconnect liveReconnectOperation, target runtimeReconnectTarget, action bridge.ReconnectAction) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, liveReconnectTimeout)
	defer cancel()
	result, err := reconnect(ctx, target.Process, target.Account.Tenant, target.Account.AppID, action)
	switch {
	case err == nil && result.State == bridge.ReconnectStarted:
		return http.StatusOK, "T3 MCP 已核验，飞书 WebSocket 正在连接。请在运行状态中查看就绪结果。"
	case err == nil && result.State == bridge.ReconnectRecoveryRequested:
		return http.StatusOK, "已受理恢复请求。请在运行状态中核对连接。"
	case err == nil && result.State == bridge.ReconnectWorkerOffline:
		return http.StatusConflict, "此 App 离线，等待设置未变。请返回运行状态，选择立即重连。"
	case err == nil && result.State == bridge.ReconnectPending:
		return http.StatusOK, "此 App 已在等待重连，原等待继续有效。"
	case err == nil && result.State == bridge.ReconnectScheduled:
		return http.StatusOK, "已登记等待重连。等待在途 T3 运行收口期间，此 App 暂停新派发。"
	case err == nil && result.State == bridge.ReconnectCancelled:
		return http.StatusOK, "已取消等待重连，此 App 恢复派发。"
	case err == nil && result.State == bridge.ReconnectIdle:
		return http.StatusOK, "此 App 当前没有待取消的重连。"
	default:
		return http.StatusBadGateway, "重连结果未确认。请先查看运行状态，再决定是否重新发送重连请求。"
	}
}
