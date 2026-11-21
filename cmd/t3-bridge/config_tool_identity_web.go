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
)

//go:embed config_tool_identity_web.html
var configToolIdentityHTML string

type configToolIdentityPage struct {
	Sources []webSource
	Source  webSource
	Choices []workspaceToolIdentity
	Checked *workspaceToolIdentity
	Error   string
}

func configToolIdentityHandler(sources func() ([]webSource, error),
	check func(context.Context, webSource, string, string) (workspaceToolIdentity, error),
) http.HandlerFunc {
	page := template.Must(newWorkspacePage("tool identity").Parse(configToolIdentityHTML))
	return func(w http.ResponseWriter, r *http.Request) {
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
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 2048)
			if r.URL.ForceQuery || r.URL.RawQuery != "" || r.ParseForm() != nil || len(r.PostForm) != 3 ||
				len(r.PostForm["source"]) != 1 || len(r.PostForm["revision"]) != 1 || len(r.PostForm["choice"]) != 1 {
				http.Error(w, "核验表单无效。", http.StatusBadRequest)
				return
			}
			id, revision = r.PostForm.Get("source"), r.PostForm.Get("revision")
			value := r.PostForm.Get("choice")
			var err error
			choice, err = strconv.Atoi(value)
			if id == "" || revision == "" || err != nil || choice < 0 || strconv.Itoa(choice) != value {
				http.Error(w, "请选择已保存的配置和要核验的路由。", http.StatusBadRequest)
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
		view := configToolIdentityPage{}
		for _, source := range available {
			if source.Path == "" {
				continue
			}
			current, choices, err := savedToolIdentityChoices(source.Path)
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
			status, view.Error = http.StatusConflict, errToolIdentityConfigChanged.Error()
		case post && choice >= len(view.Choices):
			status, view.Error = http.StatusBadRequest, "请选择已保存配置中的工具身份路由。"
		case post:
			selected := view.Choices[choice]
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(35 * time.Second))
			_, err := check(r.Context(), view.Source, selected.AppID, selected.RouteID)
			switch {
			case errors.Is(err, errToolIdentityConfigChanged):
				status, view.Error = http.StatusConflict, errToolIdentityConfigChanged.Error()
				view.Choices = nil
			case err != nil:
				view.Checked = &selected
				status, view.Error = http.StatusUnprocessableEntity, "请核对本机 lark-cli、路由身份绑定及飞书授权。"
				var failure toolIdentityProbeFailure
				if errors.As(err, &failure) {
					view.Error = failure.Error()
				}
			default:
				view.Checked = &selected
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}
