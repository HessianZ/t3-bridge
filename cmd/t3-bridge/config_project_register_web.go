package main

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_project_register_web.html
var configProjectRegisterHTML string

type projectRegisterRoute struct {
	AppID, RouteID, EnvironmentID, HostName string
}

type configProjectRegisterPage struct {
	Source           webSource
	Routes           []projectRegisterRoute
	Selected         *projectRegisterRoute
	Directory        string
	ManagedDirectory string
	Candidate        string
	Error            string
}

func configProjectRegisterHandler(sources func() ([]webSource, error),
	register func(context.Context, string, string, string, string, string, string) ([]byte, error),
	workspace func(string, bridge.Config, string) (string, error),
) http.HandlerFunc {
	page := template.Must(newWorkspacePage("project register").Parse(configProjectRegisterHTML))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		post := r.Method == http.MethodPost
		if !post && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !runtimeRequestOriginAllowed(r) || len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1 {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		// Native same-origin form POSTs need a non-opaque Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		var values url.Values
		var setupTokenFile string
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
			if r.URL.ForceQuery || r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "请核对并确认项目登记表单。", http.StatusBadRequest)
				return
			}
			values = r.PostForm
			setupTokenFiles := values["setupTokenFile"]
			setupTokenFile = values.Get("setupTokenFile")
			delete(values, "setupTokenFile")
			if len(setupTokenFiles) > 1 ||
				!validProjectRegisterFields(values, "source", "revision", "appId", "routeId", "directory", "confirm") {
				http.Error(w, "请核对并确认项目登记表单。", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(setupTokenFile) == "" {
				setupTokenFile = ""
			}
			switch values.Get("confirm") {
			case "register", "create-managed":
			default:
				http.Error(w, "请核对并确认项目登记表单。", http.StatusBadRequest)
				return
			}
		} else {
			query, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil || (!validProjectRegisterFields(query, "source") && !validProjectRegisterFields(query, "source", "appId", "routeId")) {
				http.Error(w, "请选择已保存的配置和路由。", http.StatusBadRequest)
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
		if err != nil {
			http.Error(w, "请选择已保存的本机私有 JSON 配置。", http.StatusConflict)
			return
		}
		source.Revision = configRevision(raw)
		if post && source.Revision != values.Get("revision") {
			http.Error(w, "配置文件版本已变化，请重新打开后登记项目。", http.StatusConflict)
			return
		}
		config, err := bridge.ParseConfig(raw)
		if err != nil {
			http.Error(w, "请先校验并保存有效的配置。", http.StatusConflict)
			return
		}
		view := configProjectRegisterPage{Source: source}
		appID, routeID := values.Get("appId"), values.Get("routeId")
		for _, account := range config.Accounts() {
			for _, id := range slices.Sorted(maps.Keys(account.Routes)) {
				target := account.Routes[id]
				route := projectRegisterRoute{account.AppID, id, target.EnvironmentID, target.HostName}
				view.Routes = append(view.Routes, route)
				if account.AppID == appID && id == routeID {
					view.Selected = &route
					if directory, err := workspace(source.Path, account, id); err == nil {
						view.ManagedDirectory = directory
					}
				}
			}
		}
		if appID != "" && view.Selected == nil {
			http.Error(w, "请选择已保存配置中的应用和路由。", http.StatusBadRequest)
			return
		}

		status := http.StatusOK
		if post {
			directory := values.Get("directory")
			managed := values.Get("confirm") == "create-managed"
			if managed {
				if directory != view.ManagedDirectory {
					http.Error(w, "专用工作区不可用，或目录已变化。请重新打开登记页面。", http.StatusBadRequest)
					return
				}
				directory = ""
			} else {
				view.Directory = directory
			}
			candidate, err := register(r.Context(), source.Path, source.Revision, appID, routeID, directory, setupTokenFile)
			if err == nil && managed {
				view.Directory, err = registeredProjectDirectory(candidate, appID, routeID)
			}
			if err != nil {
				status, view.Error = http.StatusUnprocessableEntity, err.Error()
				if setupTokenFile != "" {
					view.Error = strings.ReplaceAll(view.Error, setupTokenFile, "［路径已隐藏］")
				}
			} else {
				view.Candidate = string(candidate)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func registeredProjectDirectory(candidate []byte, appID, routeID string) (string, error) {
	config, err := bridge.ParseConfig(candidate)
	if err == nil {
		for _, account := range config.Accounts() {
			if directory := account.Routes[routeID].ProjectPath; account.AppID == appID && directory != "" {
				return directory, nil
			}
		}
	}
	return "", errors.New("无法读取已登记的目录，请在 T3 核对项目。")
}

func validProjectRegisterFields(values url.Values, fields ...string) bool {
	if len(values) != len(fields) {
		return false
	}
	for _, field := range fields {
		if len(values[field]) != 1 || values.Get(field) == "" {
			return false
		}
	}
	return true
}
