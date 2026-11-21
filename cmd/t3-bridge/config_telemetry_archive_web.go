package main

import (
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_telemetry_archive_web.html
var configTelemetryArchiveHTML string

type configTelemetryArchivePage struct {
	Sources                      []webSource
	SourceID                     string
	Accounts                     []receiverProcessAccount
	Account                      receiverProcessAccount
	Date, PreviousDate, NextDate string
	Limit                        int
	Records, Error               string
}

func configTelemetryArchiveHandler(sources func() ([]webSource, error),
	read func(string, receiverProcessAccount, time.Time, int) ([]byte, error)) http.HandlerFunc {
	page := template.Must(newWorkspacePage("telemetry archive").Parse(configTelemetryArchiveHTML))
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
		if err != nil {
			http.Error(w, "遥测归档查询参数无效。", http.StatusBadRequest)
			return
		}
		for field, values := range query {
			if len(values) != 1 || values[0] == "" || (field != "source" && field != "appId" && field != "date") {
				http.Error(w, "遥测归档查询参数无效。", http.StatusBadRequest)
				return
			}
		}
		date := query.Get("date")
		if date == "" {
			date = time.Now().UTC().Format(receiverTelemetryArchiveDate)
		}
		day, err := parseReceiverTelemetryArchiveDate(date)
		if err != nil {
			http.Error(w, "请输入有效的 UTC 日期（YYYY-MM-DD）。", http.StatusBadRequest)
			return
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置列表。", http.StatusConflict)
			return
		}
		view := configTelemetryArchivePage{Date: date, Limit: workspaceTelemetryArchiveLines}
		id, appID := query.Get("source"), query.Get("appId")
		var selected webSource
		for _, source := range available {
			if !filepath.IsAbs(source.Path) {
				continue
			}
			info, err := os.Lstat(source.Path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			view.Sources = append(view.Sources, source)
			if selected.ID == "" && (id == "" || source.ID == id) {
				selected = source
			}
		}
		if selected.ID == "" && (id != "" || appID != "") {
			http.Error(w, "所选配置不可用，请选择已保存的配置。", http.StatusConflict)
			return
		}

		status := http.StatusOK
		if selected.ID != "" {
			view.SourceID = selected.ID
			status = view.load(selected, appID, day, read)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func (view *configTelemetryArchivePage) load(source webSource, appID string, day time.Time,
	read func(string, receiverProcessAccount, time.Time, int) ([]byte, error)) int {
	readConfig := bridge.ReadRegularConfigJSON
	if source.Profile {
		readConfig = bridge.ReadPrivateConfigJSON
	}
	raw, err := readConfig(source.Path)
	if err != nil {
		view.Error = "无法读取所选的已保存配置。"
		return http.StatusConflict
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		view.Error = "请先校验并保存有效的配置。"
		return http.StatusUnprocessableEntity
	}
	for _, account := range config.Accounts() {
		view.Accounts = append(view.Accounts, newReceiverGroupAccount(account.Tenant, account.AppID))
	}
	account, err := selectConfigAccount(config, appID)
	if err != nil {
		view.Error = "已保存的配置中没有所选 App。"
		return http.StatusBadRequest
	}
	view.Account = newReceiverGroupAccount(account.Tenant, account.AppID)
	records, err := read(account.StateDir, view.Account, day, view.Limit)
	if err != nil {
		view.Error = "无法读取所选日期的遥测归档。"
		return http.StatusUnprocessableEntity
	}
	view.Records = string(records)
	previous := day.AddDate(0, 0, -1).Format(receiverTelemetryArchiveDate)
	if _, err := parseReceiverTelemetryArchiveDate(previous); err == nil {
		view.PreviousDate = previous
	}
	next := day.AddDate(0, 0, 1).Format(receiverTelemetryArchiveDate)
	if _, err := parseReceiverTelemetryArchiveDate(next); err == nil {
		view.NextDate = next
	}
	return http.StatusOK
}
