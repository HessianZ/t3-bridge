package main

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_workspace_import.html
var configWorkspaceImportHTML string

type configWorkspaceImportPage struct {
	Sources               []webSource
	Source                webSource
	Request               workspaceImportRequest
	Inspection            *workspaceImportInspection
	Entry                 workspaceImportDraftEntry
	Result                *workspaceImportResult
	Review, Notice, Error string
}

func configWorkspaceImportHandler(sources func() ([]webSource, error)) http.HandlerFunc {
	page := template.Must(newWorkspacePage("workspace import").Funcs(template.FuncMap{
		"encodeSourceKey": func(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) },
	}).Parse(configWorkspaceImportHTML))
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		view := configWorkspaceImportPage{}
		id, action, review := r.URL.Query().Get("source"), "", ""
		post := r.Method == http.MethodPost
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 3*bridge.MaxConfigBytes+16*1024)
			if err := r.ParseForm(); err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, "迁移表单过大", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "无法读取迁移表单", http.StatusBadRequest)
				}
				return
			}
			id, action, review = r.PostForm.Get("source"), r.PostForm.Get("action"), r.PostForm.Get("review")
			if r.URL.RawQuery != "" || !validWorkspaceImportWebForm(r.PostForm, action) {
				http.Error(w, "迁移表单无效", http.StatusBadRequest)
				return
			}
			if id == "" || (action != "preview" && action != "apply" && action != "inspect" && action != "add") {
				http.Error(w, "请选择目标配置及迁移操作", http.StatusBadRequest)
				return
			}
			view.Request = workspaceImportRequest{
				AppID: r.PostForm.Get("appId"), LegacyPath: r.PostForm.Get("legacyPath"), MappingJSON: r.PostForm.Get("mapping"),
			}
			if len(view.Request.MappingJSON) > bridge.MaxConfigBytes {
				http.Error(w, "映射 JSON 超过 1 MiB", http.StatusRequestEntityTooLarge)
				return
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
		if post {
			switch {
			case view.Source.ID == "":
				status, view.Error = http.StatusConflict, "所选配置不可用，请选择已保存的目标配置。"
			case action == "apply" && review == "":
				status, view.Error = http.StatusBadRequest, "请先预览工作区迁移。"
			case action == "inspect" || action == "add":
				inspection, err := inspectWorkspaceImport(view.Source, view.Request)
				if err == nil {
					view.Inspection = &inspection
					if action == "add" {
						err = addWorkspaceImportWebMapping(&view, r.PostForm)
					}
				}
				if err != nil {
					status, view.Error = http.StatusUnprocessableEntity, err.Error()
				}
			default:
				var result workspaceImportResult
				var nextReview string
				if action == "apply" {
					result, err = applyWorkspaceImport(view.Source, view.Request, review)
				} else {
					result, nextReview, err = previewWorkspaceImport(view.Source, view.Request)
				}
				switch {
				case errors.Is(err, errWorkspaceImportChanged):
					status, view.Error = http.StatusConflict, err.Error()
				case err != nil:
					status, view.Error = http.StatusUnprocessableEntity, err.Error()
				default:
					view.Result, view.Review = &result, nextReview
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func validWorkspaceImportWebForm(form url.Values, action string) bool {
	fields := []string{"source", "action", "appId", "legacyPath", "mapping", "review"}
	if action == "add" || len(form) != len(fields) {
		fields = append(fields, "sourceKey", "userId", "chatId", "chatType", "topicId", "routeId", "projectId")
	}
	if len(form) != len(fields) {
		return false
	}
	for _, field := range fields {
		if len(form[field]) != 1 {
			return false
		}
	}
	return true
}

func addWorkspaceImportWebMapping(view *configWorkspaceImportPage, form url.Values) error {
	key, err := base64.RawURLEncoding.DecodeString(form.Get("sourceKey"))
	view.Entry = workspaceImportDraftEntry{
		SourceKey: string(key), UserID: form.Get("userId"), ChatID: form.Get("chatId"), ChatType: form.Get("chatType"),
		TopicID: form.Get("topicId"), RouteID: form.Get("routeId"), ProjectID: form.Get("projectId"),
	}
	index := slices.IndexFunc(view.Inspection.Aliases, func(alias bridge.LegacyWorkspaceAlias) bool {
		return alias.SourceKey == view.Entry.SourceKey
	})
	if err != nil || index < 0 {
		return errors.New("请选择当前来源中的旧别名")
	}
	if !slices.Contains(view.Inspection.Routes, view.Entry.RouteID) {
		return errors.New("请选择目标应用已配置的路由")
	}
	if view.Inspection.Aliases[index].TopicRequired && view.Entry.TopicID == "" {
		return errors.New("旧话题别名需要填写目标话题 ID")
	}
	draft, err := appendWorkspaceImportMapping(view.Request.MappingJSON, view.Entry)
	if err != nil {
		return err
	}
	view.Request.MappingJSON = draft
	view.Entry = workspaceImportDraftEntry{}
	view.Notice = "已追加一条映射到草稿，请预览迁移。"
	return nil
}
