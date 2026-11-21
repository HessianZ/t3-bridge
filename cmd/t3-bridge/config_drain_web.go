package main

import (
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_drain_web.html
var configDrainHTML string

type configDrainPage struct {
	Sources []webSource
	Source  webSource
	Report  *bridge.DrainReport
	Error   string
}

func configDrainHandler(sources func() ([]webSource, error), check func(path, revision string) (bridge.DrainReport, error)) http.HandlerFunc {
	page := template.Must(newWorkspacePage("receiver drain").Funcs(template.FuncMap{
		"drainStatus":         drainStatusText,
		"drainBlocker":        drainBlockerText,
		"drainDeliverySample": drainDeliverySampleText,
	}).Parse(configDrainHTML))
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
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 2048)
			if r.URL.ForceQuery || r.URL.RawQuery != "" || r.ParseForm() != nil || len(r.PostForm) != 2 ||
				len(r.PostForm["source"]) != 1 || len(r.PostForm["revision"]) != 1 {
				http.Error(w, "排空检查表单无效。", http.StatusBadRequest)
				return
			}
			id, revision = r.PostForm.Get("source"), r.PostForm.Get("revision")
			if id == "" || revision == "" {
				http.Error(w, "请选择已保存的配置。", http.StatusBadRequest)
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
		view := configDrainPage{}
		for _, source := range available {
			if !filepath.IsAbs(source.Path) {
				continue
			}
			raw, err := bridge.ReadPrivateConfigJSON(source.Path)
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

		const configChanged = "已保存的配置已更新或不可用，请重新打开配置后检查。"
		status := http.StatusOK
		switch {
		case id != "" && view.Source.ID == "", post && revision != view.Source.Revision:
			status, view.Error = http.StatusConflict, configChanged
			view.Source.Revision = ""
		case post:
			report, err := check(view.Source.Path, revision)
			switch {
			case errors.Is(err, errWorkspaceServiceConfigChanged):
				status, view.Error = http.StatusConflict, configChanged
				view.Source.Revision = ""
			case err != nil:
				status, view.Error = http.StatusUnprocessableEntity, "无法检查排空状态。请回到配置工作台校验已保存的配置。"
			default:
				view.Report = &report
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}
