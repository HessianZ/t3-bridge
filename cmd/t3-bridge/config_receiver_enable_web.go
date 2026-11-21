package main

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_receiver_enable_web.html
var configReceiverEnableHTML string

type receiverEnableChoice struct {
	AppID, Tenant, ReceiverMachine string
}

type configReceiverEnablePage struct {
	Source    webSource
	Choices   []receiverEnableChoice
	AppID     string
	Report    *bridge.ReceiverPreflight
	Candidate string
}

func configReceiverEnableHandler(sources func() ([]webSource, error),
	prepare func(context.Context, string, string, string) (receiverEnableDraft, error),
) http.HandlerFunc {
	page := newWorkspacePage("receiver enable").Parse(configReceiverEnableHTML)
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
		var values url.Values
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.URL.ForceQuery || r.URL.RawQuery != "" || r.ParseForm() != nil ||
				!validProjectRegisterFields(r.PostForm, "source", "revision", "appId") {
				http.Error(w, "请选择已保存的配置和待启用的 App。", http.StatusBadRequest)
				return
			}
			values = r.PostForm
		} else {
			query, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil || !validProjectRegisterFields(query, "source") {
				http.Error(w, "请从接收预检页选择已保存的配置。", http.StatusBadRequest)
				return
			}
			values = query
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置。", http.StatusConflict)
			return
		}
		index := slices.IndexFunc(available, func(source webSource) bool { return source.ID == values.Get("source") })
		if index < 0 || !filepath.IsAbs(available[index].Path) {
			http.Error(w, "请选择已保存的本机私有 JSON 配置。", http.StatusConflict)
			return
		}
		source := available[index]
		raw, err := bridge.ReadPrivateConfigJSON(source.Path)
		const configChanged = "已保存的配置已更新或不可用，请从接收预检页重新打开。"
		if err != nil {
			http.Error(w, configChanged, http.StatusConflict)
			return
		}
		source.Revision = configRevision(raw)
		if post && source.Revision != values.Get("revision") {
			http.Error(w, configChanged, http.StatusConflict)
			return
		}
		config, err := bridge.ParseConfig(raw)
		if err != nil {
			http.Error(w, "请先校验并保存有效的配置。", http.StatusConflict)
			return
		}
		view := configReceiverEnablePage{Source: source, AppID: values.Get("appId")}
		for _, account := range config.Accounts() {
			if !account.ReceiverAuthorized {
				view.Choices = append(view.Choices, receiverEnableChoice{account.AppID, account.Tenant, account.ReceiverMachine})
			}
		}
		if post {
			if !slices.ContainsFunc(view.Choices, func(choice receiverEnableChoice) bool { return choice.AppID == view.AppID }) {
				http.Error(w, "请选择已保存配置中尚未启用接收的 App。", http.StatusBadRequest)
				return
			}
			response := http.NewResponseController(w)
			_ = response.SetWriteDeadline(time.Time{})
			draft, err := prepare(r.Context(), source.Path, source.Revision, view.AppID)
			_ = response.SetWriteDeadline(time.Now().Add(configWebWriteTimeout))
			switch {
			case errors.Is(err, errWorkspaceServiceConfigChanged):
				http.Error(w, configChanged, http.StatusConflict)
				return
			case err != nil:
				http.Error(w, "无法准备启用接收。请核对已保存的配置。", http.StatusUnprocessableEntity)
				return
			}
			view.Report = &draft.Report
			if outcome := draft.Report.AllAccounts; outcome != nil && outcome.Passed {
				view.Candidate = string(draft.Candidate)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, r, view)
	}
}
