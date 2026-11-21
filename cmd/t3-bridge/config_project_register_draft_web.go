package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_project_register_draft_web.html
var configDraftProjectRegisterHTML string

type configDraftProjectRegister struct {
	page      *template.Template
	sources   func() ([]webSource, error)
	register  func(context.Context, string, string, []byte, string, string, string, string) ([]byte, error)
	workspace func(string, bridge.Config, string) (string, error)
	mu        sync.Mutex
	pending   *draftProjectRegistration
}

type draftProjectRegistration struct {
	Source           webSource
	Route            projectRegisterRoute
	Draft            string
	ManagedDirectory string
	Directory        string
	Managed          bool
	SetupAuthorized  bool
	Ticket           string
	Candidate        string
	Error            string
	account          bridge.Config
	setupTokenFile   string
	expires          time.Time
}

func newConfigDraftProjectRegister(sources func() ([]webSource, error),
	register func(context.Context, string, string, []byte, string, string, string, string) ([]byte, error),
	workspace func(string, bridge.Config, string) (string, error),
) *configDraftProjectRegister {
	return &configDraftProjectRegister{page: template.Must(newWorkspacePage("draft project register").Parse(configDraftProjectRegisterHTML)),
		sources: sources, register: register, workspace: workspace}
}

func validDraftProjectStart(r *http.Request, sourceID string) bool {
	query, form := r.URL.Query(), r.PostForm
	return validProjectRegisterFields(query, "source") && query.Get("source") == sourceID &&
		len(form["projectSource"]) == 1 && form.Get("projectSource") == sourceID &&
		len(form["revision"]) == 1 && len(form["config"]) == 1 && len(form["edits"]) <= 1 &&
		len(form["projectAppId"]) == 1 && form.Get("projectAppId") != "" &&
		len(form["projectRouteId"]) == 1 && form.Get("projectRouteId") != ""
}

func (h *configDraftProjectRegister) start(w http.ResponseWriter, source webSource, revision string, draft []byte, config bridge.Config, appID, routeID string) (int, string) {
	if !filepath.IsAbs(source.Path) || revision == "" || revision != source.Revision || !projectRegistrationSourceMatches(source.Path, revision) {
		return http.StatusConflict, "请重新打开所选本机 JSON 配置并核对来源版本。"
	}
	for _, account := range config.Accounts() {
		target, exists := account.Routes[routeID]
		if account.AppID != appID || !exists {
			continue
		}
		source.JSON = nil
		view := draftProjectRegistration{Source: source, Draft: string(draft), account: account,
			Route: projectRegisterRoute{appID, routeID, target.EnvironmentID, target.HostName}}
		if directory, err := h.workspace(source.Path, account, routeID); err == nil {
			view.ManagedDirectory = directory
		}
		h.render(w, http.StatusOK, h.issue(view))
		return http.StatusOK, ""
	}
	return http.StatusBadRequest, "草稿中没有所选应用和路由。请核对后重新打开项目登记。"
}

func (h *configDraftProjectRegister) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !runtimeRequestOriginAllowed(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	if r.URL.ForceQuery || r.URL.RawQuery != "" || r.ParseForm() != nil {
		http.Error(w, "项目登记表单无效。", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	action, mode := form.Get("action"), form.Get("mode")
	directory, setupTokenFile := form.Get("directory"), form.Get("setupTokenFile")
	switch action {
	case "preview":
		validOptions := len(form["directory"]) == 1 && len(form["setupTokenFile"]) <= 1
		delete(form, "directory")
		delete(form, "setupTokenFile")
		if !validOptions || !validProjectRegisterFields(form, "action", "ticket", "mode") || mode != "existing" && mode != "managed" {
			http.Error(w, "项目登记表单无效。", http.StatusBadRequest)
			return
		}
	case "confirm":
		if !validProjectRegisterFields(form, "action", "ticket", "confirm") || form.Get("confirm") != "register" {
			http.Error(w, "请先核对预览并确认登记。", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "项目登记表单无效。", http.StatusBadRequest)
		return
	}
	view := h.take(form.Get("ticket"))
	if view == nil || (action == "confirm") != (view.Directory != "") {
		http.Error(w, "项目登记预览已失效。请返回草稿并重新预览。", http.StatusConflict)
		return
	}
	view.Ticket = ""
	if !h.sourceCurrent(view.Source) {
		view.Error = "配置来源或版本已变化。请重新打开所选配置后核对草稿。"
		h.render(w, http.StatusConflict, *view)
		return
	}
	if action == "preview" {
		status := http.StatusOK
		if err := h.preview(view, mode == "managed", directory, setupTokenFile); err != nil {
			status, view.Error = http.StatusUnprocessableEntity, err.Error()
		}
		h.render(w, status, h.issue(*view))
		return
	}
	directory = view.Directory
	if view.Managed {
		actual, err := h.workspace(view.Source.Path, view.account, view.Route.RouteID)
		if err != nil || actual != directory {
			view.Error = "专用工作区路径已变化或不可用。请重新预览。"
			h.render(w, http.StatusConflict, *view)
			return
		}
		directory = ""
	}
	candidate, err := h.register(r.Context(), view.Source.Path, view.Source.Revision, []byte(view.Draft),
		view.Route.AppID, view.Route.RouteID, directory, view.setupTokenFile)
	if err != nil {
		view.Error = err.Error()
		if view.setupTokenFile != "" {
			view.Error = strings.ReplaceAll(view.Error, view.setupTokenFile, "［路径已隐藏］")
		}
		h.render(w, http.StatusUnprocessableEntity, *view)
		return
	}
	view.Candidate = string(candidate)
	h.render(w, http.StatusOK, *view)
}

func (h *configDraftProjectRegister) sourceCurrent(source webSource) bool {
	available, err := h.sources()
	return err == nil && slices.ContainsFunc(available, func(current webSource) bool {
		return current.ID == source.ID && current.Path == source.Path
	}) && projectRegistrationSourceMatches(source.Path, source.Revision)
}

func (h *configDraftProjectRegister) preview(view *draftProjectRegistration, managed bool, directory, setupTokenFile string) error {
	if managed {
		actual, err := h.workspace(view.Source.Path, view.account, view.Route.RouteID)
		if err != nil || actual == "" || actual != view.ManagedDirectory {
			return errors.New("专用工作区路径已变化或不可用。请返回草稿重新打开项目登记。")
		}
		directory = actual
	} else if !bridge.IsAbsoluteWorkspacePath(directory) {
		return errors.New("请填写目标机器上已有目录的绝对路径。")
	}
	if strings.TrimSpace(setupTokenFile) == "" {
		setupTokenFile = ""
	}
	if _, err := view.account.MCP.WithProjectSetupToken(setupTokenFile); err != nil {
		return err
	}
	view.Directory, view.Managed, view.setupTokenFile = directory, managed, setupTokenFile
	view.SetupAuthorized = setupTokenFile != ""
	view.Error = ""
	return nil
}

func (h *configDraftProjectRegister) issue(view draftProjectRegistration) draftProjectRegistration {
	h.mu.Lock()
	defer h.mu.Unlock()
	view.Ticket = rand.Text()
	view.expires = time.Now().Add(5 * time.Minute)
	h.pending = &view
	return view
}

func (h *configDraftProjectRegister) take(ticket string) *draftProjectRegistration {
	h.mu.Lock()
	defer h.mu.Unlock()
	view := h.pending
	if view == nil || view.Ticket != ticket {
		return nil
	}
	h.pending = nil
	if !time.Now().Before(view.expires) {
		return nil
	}
	return view
}

func (h *configDraftProjectRegister) render(w http.ResponseWriter, status int, view draftProjectRegistration) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = h.page.Execute(w, view)
}
