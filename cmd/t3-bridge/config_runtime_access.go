package main

import (
	"context"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_runtime_access.html
var configRuntimeAccessHTML string

type runtimeAccessTarget runtimeSettingsTarget

func (t runtimeAccessTarget) URL() string {
	return "/runtime/access?" + url.Values{
		"process": {t.Process}, "tenant": {t.Account.Tenant}, "appId": {t.Account.AppID},
	}.Encode()
}

func (t runtimeAccessTarget) editURL(entry bridge.LocalAccessPolicyEntry) string {
	return t.URL() + "&" + url.Values{
		"kind": {entry.Kind}, "userId": {entry.UserID}, "routeId": {entry.RouteID},
	}.Encode() + "#edit-access"
}

func configRuntimeAccessHandler(list func() ([]receiverProcess, error), apply func(context.Context, string, string, string, bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error)) http.HandlerFunc {
	page := template.Must(newWorkspacePage("runtime access").Funcs(template.FuncMap{
		"editURL": runtimeAccessTarget.editURL,
		"kindLabel": func(kind string) string {
			switch kind {
			case "admin":
				return "管理员"
			case "route":
				return "路由"
			default:
				return "用户"
			}
		},
		"modeLabel": func(mode string) string {
			switch mode {
			case "allow":
				return "允许"
			case "deny":
				return "撤销"
			default:
				return "继承"
			}
		},
	}).Parse(configRuntimeAccessHTML))
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		post := r.Method == http.MethodPost
		values := r.URL.Query()
		var request bridge.LocalAccessPolicyRequest
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.URL.RawQuery != "" || r.ParseForm() != nil || !validRuntimeAccessForm(r.PostForm) {
				http.Error(w, "在线授权请求不完整。", http.StatusBadRequest)
				return
			}
			values = r.PostForm
			request = runtimeAccessRule(values)
			request.Mode = values.Get("mode")
			if err := request.ValidateChange(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		var view struct {
			Targets  []runtimeAccessTarget
			Selected *runtimeAccessTarget
			Policy   *bridge.LocalAccessPolicyView
			Edit     bridge.LocalAccessPolicyRequest
			Notice   string
		}
		status := http.StatusOK
		processes, err := list()
		if err != nil {
			status, view.Notice = http.StatusServiceUnavailable, "接收进程清单暂不可用。"
		} else {
			for _, process := range processes {
				if !process.AccessPolicy {
					continue
				}
				for _, account := range process.Accounts {
					view.Targets = append(view.Targets, runtimeAccessTarget{Process: process.ID, Account: account, PID: process.PID})
				}
			}
			selected := -1
			switch {
			case post || len(values) != 0:
				selected = slices.IndexFunc(view.Targets, func(target runtimeAccessTarget) bool {
					return target.Process == values.Get("process") && target.Account.Tenant == values.Get("tenant") &&
						target.Account.AppID == values.Get("appId")
				})
				if selected < 0 {
					status, view.Notice = http.StatusNotFound, "该接收进程或 App 已不可用，请重新选择。"
				}
			case len(view.Targets) == 1:
				selected = 0
			}
			if selected >= 0 {
				target := view.Targets[selected]
				view.Selected = &target
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				policy, err := apply(ctx, target.Process, target.Account.Tenant, target.Account.AppID, request)
				cancel()
				switch {
				case err != nil && post:
					status, view.Notice = http.StatusBadGateway, "授权保存未获确认，请重新读取生效值。"
				case err != nil:
					status, view.Notice = http.StatusBadGateway, "暂时无法读取在线权限。"
				case post:
					http.Redirect(w, r, target.URL()+"&updated=1#access-rules", http.StatusSeeOther)
					return
				default:
					view.Policy = &policy
					view.Edit = runtimeAccessRule(values)
					if values.Get("updated") == "1" {
						view.Notice = "在线权限已保存。"
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func runtimeAccessRule(values url.Values) bridge.LocalAccessPolicyRequest {
	return bridge.LocalAccessPolicyRequest{Kind: values.Get("kind"), UserID: values.Get("userId"), RouteID: values.Get("routeId")}
}

func validRuntimeAccessForm(values url.Values) bool {
	keys := []string{"process", "tenant", "appId", "kind", "userId", "routeId", "mode"}
	if len(values) != len(keys) || !receiverProcessID.MatchString(values.Get("process")) ||
		!slices.Contains([]string{"feishu", "lark"}, values.Get("tenant")) || values.Get("appId") == "" {
		return false
	}
	for _, key := range keys {
		if len(values[key]) != 1 {
			return false
		}
	}
	return true
}
