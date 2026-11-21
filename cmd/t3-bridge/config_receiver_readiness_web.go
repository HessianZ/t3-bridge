package main

import (
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_receiver_readiness_web.html
var configReceiverReadinessHTML string

type configReceiverReadinessPage struct {
	Sources []webSource
	Source  webSource
	Report  *receiverReadinessReport
	Error   string
}

func configReceiverReadinessHandler(sources func() ([]webSource, error), read func(path, revision string) (receiverReadinessReport, error)) http.HandlerFunc {
	page := newWorkspacePage("receiver readiness").Funcs(template.FuncMap{
		"readinessStatus": receiverReadinessStatusText,
		"receiverFailure": receiverFailureText,
		"localTime":       func(t time.Time) string { return t.Local().Format(time.DateTime) },
		"dateTime":        func(t time.Time) string { return t.Format(time.RFC3339) },
	}).Parse(configReceiverReadinessHTML)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !runtimeRequestOriginAllowed(r) || len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1 {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || (len(query) != 0 && !validProjectRegisterFields(query, "source")) {
			http.Error(w, "请选择已保存的配置。", http.StatusBadRequest)
			return
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置列表。", http.StatusConflict)
			return
		}
		view := configReceiverReadinessPage{}
		id := query.Get("source")
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
			if view.Source.ID == "" && (id == "" || id == source.ID) {
				view.Source = source
			}
		}

		const configChanged = "已保存的配置已更新或不可用。请刷新状态或重新选择配置。"
		status := http.StatusOK
		switch {
		case id != "" && view.Source.ID == "":
			status, view.Error = http.StatusConflict, configChanged
		case view.Source.ID != "":
			report, err := read(view.Source.Path, view.Source.Revision)
			switch {
			case errors.Is(err, errWorkspaceServiceConfigChanged):
				status, view.Error = http.StatusConflict, configChanged
			case err != nil:
				status, view.Error = http.StatusUnprocessableEntity, "无法读取接收就绪状态。请核对已保存的配置和本机接收进程。"
			default:
				view.Report = &report
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}
