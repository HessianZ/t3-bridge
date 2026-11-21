package main

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

var errWorkspaceServiceConfigChanged = errors.New("saved configuration changed")

func installWorkspaceService(instance string, source webSource) error {
	if !serviceInstanceName.MatchString(instance) {
		return errors.New("invalid service instance name")
	}
	if source.Path == "" || source.Revision == "" || source.Revision == "missing" {
		return errWorkspaceServiceConfigChanged
	}
	raw, err := bridge.ReadPrivateConfigJSON(source.Path)
	if err != nil || configRevision(raw) != source.Revision {
		return errWorkspaceServiceConfigChanged
	}
	return runHostService([]string{"install", "-instance", instance, "-config", source.Path}, io.Discard)
}

//go:embed config_service_install.html
var configServiceInstallHTML string

type configServiceInstallPage struct {
	Sources                          []webSource
	Source                           webSource
	Binary, Instance, Message, AppID string
	Installed                        bool
	Apps                             []servicePreflightApp
	Preflight                        *bridge.ReceiverPreflight
}

type servicePreflightApp struct {
	AppID, Tenant string
	Enabled       bool
}

func configServiceInstallHandler(sources func() ([]webSource, error), install func(string, webSource) error,
	preflight func(context.Context, string, string, bool) (bridge.ReceiverPreflight, error),
	preflightApp func(context.Context, string, string, string, bool) (bridge.ReceiverPreflight, error),
) http.HandlerFunc {
	page := newWorkspacePage("service install").Parse(configServiceInstallHTML)
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		id, revision, action := r.URL.Query().Get("source"), "", ""
		post := r.Method == http.MethodPost
		view := configServiceInstallPage{}
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 2048)
			const invalidForm = "请填写有效的实例名并确认已保存的配置。"
			if r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, invalidForm, http.StatusBadRequest)
				return
			}
			action = r.PostForm.Get("action")
			if action == "preflight-app" {
				if r.URL.ForceQuery || !validProjectRegisterFields(r.PostForm, "source", "revision", "appId", "action") {
					http.Error(w, "请选择已保存的配置和一个 App。", http.StatusBadRequest)
					return
				}
				view.AppID = r.PostForm.Get("appId")
			} else {
				if len(r.PostForm) != 4 || len(r.PostForm["source"]) != 1 || len(r.PostForm["revision"]) != 1 ||
					len(r.PostForm["instance"]) != 1 || len(r.PostForm["action"]) != 1 {
					http.Error(w, invalidForm, http.StatusBadRequest)
					return
				}
				view.Instance = r.PostForm.Get("instance")
				if (action != "install" && action != "preflight" && action != "preflight-all") ||
					(action == "install" && !serviceInstanceName.MatchString(view.Instance)) {
					http.Error(w, "请填写有效的实例名并选择操作。", http.StatusBadRequest)
					return
				}
			}
			id, revision = r.PostForm.Get("source"), r.PostForm.Get("revision")
			if id == "" || revision == "" {
				http.Error(w, "请确认已保存的配置。", http.StatusBadRequest)
				return
			}
		}
		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置。", http.StatusConflict)
			return
		}
		var selectedJSON []byte
		for _, source := range available {
			if source.Path == "" {
				continue
			}
			raw, err := bridge.ReadPrivateConfigJSON(source.Path)
			if err == nil {
				source.Revision = configRevision(raw)
				view.Sources = append(view.Sources, source)
				if view.Source.Path == "" && (id == "" || id == source.ID) {
					view.Source, selectedJSON = source, raw
				}
			}
		}
		if view.Source.Path != "" {
			if config, err := bridge.ParseConfig(selectedJSON); err == nil {
				for _, account := range config.Accounts() {
					view.Apps = append(view.Apps, servicePreflightApp{account.AppID, account.Tenant, account.ReceiverAuthorized})
				}
			}
		}
		view.Binary, _ = os.Executable()
		status := http.StatusOK
		if post {
			switch {
			case view.Source.Path == "" || revision != view.Source.Revision:
				status, view.Message = http.StatusConflict, "配置文件已更新或不可用，请重新选择并确认。"
			case action == "preflight-app" && len(view.Apps) == 0:
				status, view.Message = http.StatusUnprocessableEntity, "预检无法读取有效的配置。请回到配置工作台校验。"
			case action == "preflight-app" && !slices.ContainsFunc(view.Apps, func(app servicePreflightApp) bool { return app.AppID == view.AppID }):
				status, view.Message = http.StatusBadRequest, "请选择已保存配置中的 App。"
			case action != "install":
				response := http.NewResponseController(w)
				_ = response.SetWriteDeadline(time.Time{})
				var report bridge.ReceiverPreflight
				var err error
				if action == "preflight-app" {
					report, err = preflightApp(r.Context(), view.Source.Path, view.Source.Revision, view.AppID, true)
				} else {
					report, err = preflight(r.Context(), view.Source.Path, view.Source.Revision, action == "preflight-all")
				}
				_ = response.SetWriteDeadline(time.Now().Add(configWebWriteTimeout))
				switch {
				case errors.Is(err, errWorkspaceServiceConfigChanged):
					status, view.Message = http.StatusConflict, "配置文件已更新或不可用，请重新选择并确认。"
				case err != nil:
					status, view.Message = http.StatusUnprocessableEntity, "预检无法读取有效的配置。请回到配置工作台校验。"
				default:
					view.Preflight = &report
				}
			default:
				response := http.NewResponseController(w)
				_ = response.SetWriteDeadline(time.Time{})
				err := install(view.Instance, view.Source)
				_ = response.SetWriteDeadline(time.Now().Add(configWebWriteTimeout))
				switch {
				case errors.Is(err, errWorkspaceServiceConfigChanged):
					status, view.Message = http.StatusConflict, "配置文件已更新或不可用，请重新选择并确认。"
				case err != nil:
					status, view.Message = http.StatusBadGateway, "服务安装未确认。请核对实例名、配置校验和系统服务状态。"
				default:
					view.Installed, view.Message = true, "后台服务已安装。可到运行页启动。"
				}
			}
		}
		if view.Instance == "" && view.Source.Path != "" {
			name := filepath.Base(filepath.Dir(view.Source.Path))
			if view.Source.Profile && serviceInstanceName.MatchString(name) {
				view.Instance = name
			}
		}
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}
