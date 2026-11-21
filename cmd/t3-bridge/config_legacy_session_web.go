package main

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"path/filepath"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_legacy_session_web.html
var configLegacySessionHTML string

type configLegacySessionPage struct {
	Sources       []webSource
	Source        webSource
	Request       legacySessionRequest
	Inspection    *legacySessionInspection
	ChatIDs       []string
	Result        *bridge.LegacySessionImportResult
	CheckLines    []string
	Candidate     string
	Review, Error string
}

func configLegacySessionHandler(sources func() ([]webSource, error)) http.HandlerFunc {
	return configLegacySessionHandlerWithActions(sources, previewVerifiedLegacySession, prepareLegacySessionRoute)
}

func configLegacySessionHandlerWithActions(sources func() ([]webSource, error),
	check func(context.Context, webSource, legacySessionRequest) (bridge.LegacySessionImportResult, string, []string, error),
	prepareRoute func(context.Context, webSource, legacySessionRequest) ([]byte, error),
) http.HandlerFunc {
	page := template.Must(newWorkspacePage("legacy session import").Funcs(template.FuncMap{
		"updatedAt": func(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.DateTime + " UTC") },
	}).Parse(configLegacySessionHTML))
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		// Native same-origin form POSTs need a non-opaque Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		view := configLegacySessionPage{}
		id, action, review := r.URL.Query().Get("source"), "", ""
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
			if err := r.ParseForm(); err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, "转接表单过大", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "无法读取转接表单", http.StatusBadRequest)
				}
				return
			}
			fields := []string{"source", "action", "review", "appId", "legacyConfig", "legacyProfile", "threadId", "chatId", "userId", "routeId"}
			optionalFields := []string{"chatType", "topicId", "revision"}
			fieldCount := len(fields)
			for _, field := range optionalFields {
				if values, present := r.PostForm[field]; present {
					if len(values) != 1 {
						http.Error(w, "转接表单无效", http.StatusBadRequest)
						return
					}
					fieldCount++
				}
			}
			if r.URL.RawQuery != "" || len(r.PostForm) != fieldCount {
				http.Error(w, "转接表单无效", http.StatusBadRequest)
				return
			}
			for _, field := range fields {
				if len(r.PostForm[field]) != 1 {
					http.Error(w, "转接表单无效", http.StatusBadRequest)
					return
				}
			}
			id, action, review = r.PostForm.Get("source"), r.PostForm.Get("action"), r.PostForm.Get("review")
			if id == "" || (action != "inspect" && action != "preview" && action != "check" && action != "apply" && action != "prepare-route") {
				http.Error(w, "请选择目标配置及转接操作", http.StatusBadRequest)
				return
			}
			if action == "prepare-route" && r.PostForm.Get("revision") == "" {
				http.Error(w, "请重新打开已保存的目标配置。", http.StatusBadRequest)
				return
			}
			chatType := r.PostForm.Get("chatType")
			if chatType == "p2p" {
				// Explicit private and old forms share a review fingerprint.
				chatType = ""
			}
			view.Request = legacySessionRequest{
				AppID: r.PostForm.Get("appId"), LegacyConfig: r.PostForm.Get("legacyConfig"), LegacyProfile: r.PostForm.Get("legacyProfile"),
				ThreadID: r.PostForm.Get("threadId"), ChatID: r.PostForm.Get("chatId"), UserID: r.PostForm.Get("userId"), RouteID: r.PostForm.Get("routeId"),
				ChatType: chatType, TopicID: r.PostForm.Get("topicId"),
			}
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置", http.StatusConflict)
			return
		}
		for _, source := range available {
			if !filepath.IsAbs(source.Path) {
				continue
			}
			raw, err := bridge.ReadRegularConfigJSON(source.Path)
			if err != nil {
				continue
			}
			source.Revision = configRevision(raw)
			view.Sources = append(view.Sources, source)
			if id == "" {
				id = source.ID
			}
			if source.ID == id {
				view.Source = source
			}
		}

		status := http.StatusOK
		if r.Method == http.MethodPost {
			switch {
			case view.Source.ID == "":
				status, view.Error = http.StatusConflict, "所选配置不可用，请选择已保存的目标配置。"
			case action == "prepare-route" && r.PostForm.Get("revision") != view.Source.Revision:
				status, view.Error = http.StatusConflict, "已保存配置版本已变化，请重新打开目标配置。"
			case action == "apply" && review == "":
				status, view.Error = http.StatusBadRequest, "请先预览旧会话转接。"
			default:
				var inspection legacySessionInspection
				var result bridge.LegacySessionImportResult
				var nextReview string
				var candidate []byte
				switch action {
				case "inspect":
					inspection, err = inspectLegacySession(view.Source, view.Request)
				case "apply":
					result, err = applyLegacySession(view.Source, view.Request, review)
				case "preview":
					result, nextReview, err = previewLegacySession(view.Source, view.Request)
				case "check":
					result, nextReview, view.CheckLines, err = check(r.Context(), view.Source, view.Request)
				case "prepare-route":
					candidate, err = prepareRoute(r.Context(), view.Source, view.Request)
				}
				switch {
				case errors.Is(err, errLegacySessionChanged):
					status, view.Error = http.StatusConflict, err.Error()
				case err != nil:
					status, view.Error = http.StatusUnprocessableEntity, err.Error()
				case action == "inspect":
					view.Inspection = &inspection
					for _, scope := range inspection.Scopes {
						if scope.TopicID == "" {
							view.ChatIDs = append(view.ChatIDs, scope.ChatID)
						}
					}
				case action == "prepare-route":
					view.Candidate = string(candidate)
				default:
					view.Result, view.Review = &result, nextReview
				}
			}
		} else if r.Method == http.MethodGet && view.Source.ID != "" {
			if path, err := detectLegacyConfig(); err == nil {
				view.Request.LegacyConfig = path
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}
