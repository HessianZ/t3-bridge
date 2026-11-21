package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

//go:embed config_web.html
var configWebHTML string

//go:embed config_web.js
var configWebJS string

const configWebWriteTimeout = 15 * time.Second

type webSource struct {
	ID, Label string
	JSON      []byte
	Path      string
	Revision  string
	Profile   bool
}

type configWebPage struct {
	Sources      []webSource
	Source       webSource
	JSON         string
	Accounts     []accountFields
	ShowFields   bool
	RawOpen      bool
	Valid        bool
	Error        string
	Reviewed     string
	BindingID    string
	Revision     string
	Saved        bool
	HasBackup    bool
	Changes      []configChange
	NewAccount   manualAccountFields
	Replacement  *manualReplacementFields
	LegacyForm   legacyConfigImport
	LegacyImport *legacyConfigImport
	LegacyReview *legacyCredentialWebReview
	LegacyCopied string
	Profiles     bool
	DraftProject bool
}

type manualAccountFields struct {
	AppID, AppSecretFile, StateDir, User, Tenant, Route string
}

type routeFields struct {
	bridge.Target
	OptionsJSON     string
	ToolContextJSON string
	ToolBinding     toolBindingDraft
}

type accountFields struct {
	bridge.Config
	Index              string
	Number             int
	InitialRuntimeJSON string
	RouteFields        map[string]routeFields
}

func runConfigWeb(args []string) error {
	path, provider, err := configWindowOptions("web", args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveConfigWorkspace(ctx, path, provider, false, func(workspaceCtx context.Context, address string) error {
		fmt.Printf("T3 Bridge · %s\n", address)
		<-workspaceCtx.Done()
		return nil
	})
}

func configWindowOptions(mode string, args []string) (path, provider string, err error) {
	flags := flag.NewFlagSet("t3-bridge config "+mode, flag.ContinueOnError)
	file := flags.String("config", "", "open or create a local JSON file")
	profile := flags.String("profile", "", "use the named configuration profile")
	legacyBridge := flags.String("legacy-bridge", "", "absolute path to the old executable for App Secret copying")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if flags.NArg() != 0 {
		return "", "", errors.New("unexpected arguments")
	}
	provider, err = legacyBridgeExecutable(*legacyBridge)
	if err != nil {
		return "", "", err
	}
	resolved, err := resolveConfigPath(*file, *profile, defaultConfigPath)
	if err != nil {
		return "", provider, err
	}
	path, err = filepath.Abs(resolved)
	return path, provider, err
}

func serveConfigWorkspace(ctx context.Context, path, provider string, desktop bool, open func(context.Context, string) error) error {
	sources, err := configWebSources(path)
	if err != nil {
		return err
	}
	profiles, _ := localProfiles()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	host := listener.Addr().String()
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	binding := &qrRegistration{ctx: ctx, register: registration.RegisterApp}
	legacy := &legacyCredentialWeb{provider: provider, copy: func(ctx context.Context, plan legacyCredentialPlan, binary string, candidate []byte) error {
		return plan.copy(ctx, binary, candidate)
	}}
	workspace := configWorkspaceHandlerWithLegacy(host, sources, profiles, configRuntimeHandler(profiles, readHostServiceInventory), legacy, binding)
	address, handler := configToolSession(host, profiles.withWorkspaceLanguage(workspace, desktop))
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      configWebWriteTimeout,
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
		stop()
	}()
	openErr := open(ctx, address)
	stop()
	_ = server.Close()
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return openErr
}

func webProfileSources(registry profileRegistry, sources []webSource) ([]webSource, error) {
	if registry.root == "" {
		return sources, nil
	}
	names, err := registry.names()
	if err != nil {
		return nil, err
	}
	sources = append([]webSource(nil), sources...)
	var preferred string
	if path := sources[0].Path; path != "" && filepath.Dir(filepath.Dir(path)) == filepath.Join(registry.root, "profiles") {
		preferred = path
		sources = sources[1:]
	}
	for _, name := range names {
		path, err := registry.existingFile(name)
		if err != nil {
			continue
		}
		source := webSource{ID: "profile-" + name, Label: "命名配置 · " + name, Path: path, Profile: true}
		if preferred == path {
			sources = append([]webSource{source}, sources...)
			continue
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func configWebSources(path string) ([]webSource, error) {
	sources := []webSource{
		{ID: "generic", Label: "单路由模板"},
		{ID: "multi-host", Label: "多路由模板"},
		{ID: "homeops", Label: "HomeOps 模板"},
	}
	for i, source := range sources {
		raw, err := loadConfigTemplate(source.ID)
		if err != nil {
			return nil, err
		}
		candidate, err := bridge.EditConfigJSON(raw, nil)
		if err != nil {
			return nil, err
		}
		sources[i].JSON, sources[i].Revision = candidate, configRevision(candidate)
	}
	if path == "" {
		return sources, nil
	}
	source, err := configFileSource(path, sources[0].JSON)
	if err != nil {
		return nil, err
	}
	return append([]webSource{source}, sources...), nil
}

func configFileSource(path string, fallback []byte) (webSource, error) {
	source := webSource{ID: "file", Label: "配置文件 · " + filepath.Base(path), Path: path}
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return webSource{}, errors.New("config must be JSON (.json)")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		source.JSON, source.Revision = fallback, "missing"
		return source, nil
	}
	raw, err := bridge.ReadConfigJSON(path)
	if err != nil {
		return webSource{}, err
	}
	source.Revision = configRevision(raw)
	if formatted, _ := bridge.EditConfigJSON(raw, nil); formatted != nil {
		raw = formatted
	}
	source.JSON = raw
	return source, nil
}

func configWebHandler(host string, sources []webSource, bindings ...*qrRegistration) http.Handler {
	return configWorkspaceHandler(host, sources, profileRegistry{}, bindings...)
}

func configWorkspaceHandler(host string, startupSources []webSource, profiles profileRegistry, bindings ...*qrRegistration) http.Handler {
	return configWorkspaceHandlerWithRuntime(host, startupSources, profiles, configRuntimeHandler(profiles, readHostServiceInventory), bindings...)
}

func configWorkspaceHandlerWithRuntime(host string, startupSources []webSource, profiles profileRegistry, runtimePage http.HandlerFunc, bindings ...*qrRegistration) http.Handler {
	return configWorkspaceHandlerWithLegacy(host, startupSources, profiles, runtimePage, &legacyCredentialWeb{}, bindings...)
}

func configWorkspaceHandlerWithLegacy(host string, startupSources []webSource, profiles profileRegistry, runtimePage http.HandlerFunc, legacy *legacyCredentialWeb, bindings ...*qrRegistration) http.Handler {
	page := newWorkspacePage("config web").Funcs(template.FuncMap{
		"lines":                 func(values []string) string { return strings.Join(values, "\n") },
		"fileName":              filepath.Base,
		"validReplacementAppID": validReplacementAppID,
		"newRoute": func() routeFields {
			return routeFields{
				Target:      bridge.Target{RuntimeMode: "approval-required", InteractionMode: "default"},
				OptionsJSON: "{}",
			}
		},
	}).Parse(configWebHTML)
	mux := http.NewServeMux()
	binding := &qrRegistration{ctx: context.Background(), register: registration.RegisterApp}
	if len(bindings) != 0 {
		binding = bindings[0]
	}
	binding.routes(mux)
	registerT3Authorization(mux, binding.ctx)
	if profiles.root != "" {
		registerProfileWebRoutes(mux, profiles)
	}
	mux.HandleFunc("GET /runtime", runtimePage)
	mux.HandleFunc("GET /runtime/logs", configRuntimeLogsHandler(readLocalServiceLogs))
	mux.HandleFunc("POST /runtime/action", runtimePage)
	workspaceImportPage := configWorkspaceImportHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	})
	mux.HandleFunc("GET /workspace-import", workspaceImportPage)
	mux.HandleFunc("POST /workspace-import", workspaceImportPage)
	legacySessionPage := configLegacySessionHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	})
	mux.HandleFunc("GET /legacy-session-import", legacySessionPage)
	mux.HandleFunc("POST /legacy-session-import", legacySessionPage)
	messageReceiptPage := configMessageReceiptHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	})
	mux.HandleFunc("GET /message-receipt", messageReceiptPage)
	mux.HandleFunc("POST /message-receipt", messageReceiptPage)
	mux.HandleFunc("/telemetry-archive", configTelemetryArchiveHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, readReceiverTelemetryArchive))
	drainPage := configDrainHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, checkSavedDrain)
	mux.HandleFunc("GET /drain-check", drainPage)
	mux.HandleFunc("POST /drain-check", drainPage)
	credentialsPage := configCredentialsHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	})
	mux.HandleFunc("GET /credentials", credentialsPage)
	mux.HandleFunc("POST /credentials", credentialsPage)
	toolIdentityPage := configToolIdentityHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, checkSavedToolIdentity)
	mux.HandleFunc("GET /tool-identity", toolIdentityPage)
	mux.HandleFunc("POST /tool-identity", toolIdentityPage)
	meetingPreflightPage := configMeetingPreflightHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, checkSavedMeetingPermissions, checkSavedMeetingAvailability)
	mux.HandleFunc("GET /meeting-preflight", meetingPreflightPage)
	mux.HandleFunc("POST /meeting-preflight", meetingPreflightPage)
	projectRegisterPage := configProjectRegisterHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, registerSavedProject, managedProjectWorkspace)
	mux.HandleFunc("GET /project-register", projectRegisterPage)
	mux.HandleFunc("POST /project-register", projectRegisterPage)
	draftProject := newConfigDraftProjectRegister(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, registerUnsavedProject, managedProjectWorkspace)
	mux.HandleFunc("POST /project-register/draft", draftProject.serve)
	mux.HandleFunc("POST /tool-binding/draft", configToolBindingDraftHandler)
	installPage := configServiceInstallHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, installWorkspaceService, checkSavedReceiverConfig, checkSavedReceiverAppConfig)
	mux.HandleFunc("GET /runtime/install", installPage)
	mux.HandleFunc("POST /runtime/install", installPage)
	receiverEnablePage := configReceiverEnableHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, prepareReceiverEnable)
	mux.HandleFunc("GET /runtime/receiver-enable", receiverEnablePage)
	mux.HandleFunc("POST /runtime/receiver-enable", receiverEnablePage)
	mux.HandleFunc("/runtime/receiver-readiness", configReceiverReadinessHandler(func() ([]webSource, error) {
		return webProfileSources(profiles, startupSources)
	}, func(path, revision string) (receiverReadinessReport, error) {
		return savedReceiverReadiness(profiles, path, revision)
	}))
	registry := localReceiverProcesses(profiles)
	groupsPage := configRuntimeGroupsHandler(func() ([]receiverProcess, error) { return readRuntimeReceivers(profiles) }, registry.groupAccess)
	mux.HandleFunc("GET /runtime/groups", groupsPage)
	mux.HandleFunc("POST /runtime/groups", groupsPage)
	settingsPage := configRuntimeSettingsHandler(func() ([]receiverProcess, error) { return readRuntimeReceivers(profiles) }, registry.runSettings)
	mux.HandleFunc("GET /runtime/settings", settingsPage)
	mux.HandleFunc("POST /runtime/settings", settingsPage)
	reconnectPage := configRuntimeReconnectHandler(func() ([]receiverProcess, error) { return readRuntimeReceivers(profiles) }, registry.reconnect)
	mux.HandleFunc("GET /runtime/reconnect", reconnectPage)
	mux.HandleFunc("POST /runtime/reconnect", reconnectPage)
	meetingsPage := configRuntimeMeetingsHandler(func() ([]receiverProcess, error) { return readRuntimeReceivers(profiles) }, registry.meeting)
	mux.HandleFunc("GET /runtime/meetings", meetingsPage)
	mux.HandleFunc("POST /runtime/meetings", meetingsPage)
	accessPage := configRuntimeAccessHandler(func() ([]receiverProcess, error) { return readRuntimeReceivers(profiles) }, registry.accessPolicy)
	mux.HandleFunc("GET /runtime/access", accessPage)
	mux.HandleFunc("POST /runtime/access", accessPage)
	mux.HandleFunc("GET /runtime/groups.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, configRuntimeGroupsJS)
	})
	mux.HandleFunc("POST /t3/catalog", configCatalogHandler)
	mux.HandleFunc("POST /feishu/check", configFeishuCheck)
	mux.HandleFunc("POST /feishu/groups", configFeishuGroups(bridge.ListFeishuGroups))
	mux.HandleFunc("POST /feishu/my-groups", configUserGroupsHandler(func(account bridge.Config) (webUserGroupsService, error) {
		return bridge.NewUserGroups(account, profiles.root)
	}))
	mux.HandleFunc("GET /config.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, configWebJS)
	})
	mux.HandleFunc("GET /workspace.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, workspaceCSS)
	})
	mux.HandleFunc("GET /workspace-icon.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(workspaceIcon)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/workspace-icon.png", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /workspace-session.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = io.WriteString(w, configToolSessionJS)
	})
	mux.HandleFunc("GET /workspace-language.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		catalog := "{}"
		if requestWorkspaceLanguage(r) == workspaceEN {
			catalog = string(workspaceEnglishJSON)
		}
		_, _ = io.WriteString(w, "const workspaceMessages = "+catalog+";\n")
		_, _ = io.WriteString(w, workspaceLanguageJS)
	})
	mux.HandleFunc("GET /workspace-language", profiles.workspaceLanguageHandler)
	mux.HandleFunc("POST /workspace-language", profiles.workspaceLanguageHandler)
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Has("replacementSecret") {
			http.Error(w, "密钥只能在换绑表单中提交", http.StatusBadRequest)
			return
		}
		sources, err := webProfileSources(profiles, startupSources)
		if err != nil {
			http.Error(w, "无法读取命名配置列表", http.StatusConflict)
			return
		}
		source := sources[0]
		if id := query.Get("source"); id != "" {
			source = webSource{}
			for _, candidate := range sources {
				if candidate.ID == id {
					source = candidate
					break
				}
			}
			if source.ID == "" {
				http.NotFound(w, r)
				return
			}
		}

		if source.Path != "" {
			latest, err := configFileSource(source.Path, source.JSON)
			if err != nil || source.Profile && latest.Revision == "missing" {
				http.Error(w, "无法读取配置文件", http.StatusConflict)
				return
			}
			source.JSON, source.Revision = latest.JSON, latest.Revision
		}
		view := configWebPage{Sources: sources, Source: source, Revision: source.Revision, Saved: r.Method == http.MethodGet && query.Get("saved") == "1", Profiles: profiles.root != "", DraftProject: filepath.IsAbs(source.Path)}
		view.HasBackup = hasConfigBackup(source.Path)
		candidate := source.JSON
		var editErr error
		var action string
		switch r.Method {
		case http.MethodGet:
			if slices.Contains(query["action"], "replace-app") || slices.Contains(query["action"], "replace-app-secret") {
				w.Header().Set("Allow", "POST")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if path, err := detectLegacyConfig(); err == nil {
				view.LegacyForm.SourcePath = path
			}
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 9*bridge.MaxConfigBytes+1024)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "无法读取表单", http.StatusBadRequest)
				return
			}
			action = r.PostForm.Get("action")
			copyAction := action == "preview-legacy-credential" || action == "confirm-legacy-credential" || action == "cancel-legacy-credential"
			draftProjectAction := slices.Contains(r.Form["action"], "register-draft-project")
			replaceAppAction := slices.Contains(r.Form["action"], "replace-app") || slices.Contains(r.Form["action"], "replace-app-secret")
			replaceSecret := action == "replace-app-secret"
			if !replaceSecret && r.PostForm.Has("replacementSecret") {
				http.Error(w, "密钥只能在换绑表单中提交", http.StatusBadRequest)
				return
			}
			localT3Action := slices.ContainsFunc(r.Form["action"], func(value string) bool {
				name, _, _ := strings.Cut(value, ":")
				return name == "use-local-t3"
			})
			singleAction := len(r.PostForm["action"]) == 1 && (!(localT3Action || replaceAppAction) || len(r.Form["action"]) == 1)
			if (copyAction || localT3Action || draftProjectAction || replaceAppAction) && (!singleAction || !runtimeRequestOriginAllowed(r)) {
				http.Error(w, "invalid origin or action", http.StatusForbidden)
				return
			}
			if replaceAppAction {
				if (len(query) != 0 && (len(query) != 1 || len(query["source"]) != 1 || query.Get("source") != source.ID)) ||
					r.PostForm.Has("source") || len(r.PostForm["edits"]) > 1 {
					http.Error(w, "换绑表单字段无效", http.StatusBadRequest)
					return
				}
				for _, field := range []string{"config", "revision", "replaceAppId", "replacementAppId", "replacementAppSecretFile", "replacementUser", "replacementTenant"} {
					if len(r.PostForm[field]) != 1 {
						http.Error(w, "换绑表单字段缺失或重复", http.StatusBadRequest)
						return
					}
				}
				if replaceSecret && (len(r.PostForm["replacementSecret"]) != 1 || r.PostForm.Get("replacementSecret") == "" ||
					strings.TrimSpace(r.PostForm.Get("replacementAppSecretFile")) != "") {
					http.Error(w, "请只填写新密钥或已有密钥文件路径中的一项", http.StatusBadRequest)
					return
				}
				if source.Revision == "" || source.Revision != r.PostForm.Get("revision") {
					http.Error(w, "配置来源版本已变化或不可用，请重新载入所选配置后换绑", http.StatusConflict)
					return
				}
			}
			if draftProjectAction && (action != "register-draft-project" || !validDraftProjectStart(r, source.ID)) {
				http.Error(w, "项目登记表单无效，请返回草稿后重新打开。", http.StatusBadRequest)
				return
			}
			var localT3Account int
			if localT3Action {
				number := strings.TrimPrefix(action, "use-local-t3:")
				index, err := strconv.Atoi(number)
				if err != nil || index < 0 || strconv.Itoa(index) != number {
					http.Error(w, "invalid account index", http.StatusBadRequest)
					return
				}
				localT3Account = index
			}
			view.Revision = r.PostForm.Get("revision")
			candidate = []byte(r.PostForm.Get("config"))
			edits := []byte(r.PostForm.Get("edits"))
			if len(candidate) > bridge.MaxConfigBytes || len(edits) > bridge.MaxConfigBytes {
				http.Error(w, "JSON 超过 1 MiB", http.StatusRequestEntityTooLarge)
				return
			}
			var formatted []byte
			if action == "load-backup" {
				var saved []byte
				saved, editErr = readConfigBackup(source.Path)
				if editErr == nil {
					formatted, editErr = bridge.EditConfigJSON(saved, nil)
				}
			} else {
				formatted, editErr = bridge.EditConfigJSON(candidate, edits)
			}
			if formatted != nil {
				candidate = formatted
			}
			if editErr == nil && localT3Action {
				updated, err := useLocalT3(candidate, localT3Account)
				editErr = err
				if editErr == nil {
					candidate = updated
				}
			}
			view.LegacyForm = legacyConfigImport{
				SourcePath:    r.PostForm.Get("legacySourcePath"),
				Profile:       r.PostForm.Get("legacyProfile"),
				StateDir:      r.PostForm.Get("legacyStateDir"),
				AppSecretFile: r.PostForm.Get("legacyAppSecretFile"),
				ReuseT3:       r.PostForm.Get("legacyReuseT3") == "1",
			}
			if action == "import-legacy" || copyAction {
				// Completing an import can close the panel while retaining the submitted form.
				view.LegacyImport = &view.LegacyForm
			}
			if copyAction || r.PostForm.Get("legacyReview") != "" {
				candidate, editErr = legacy.apply(r.Context(), &view, action, r.PostForm.Get("legacyReview"), candidate, editErr)
			}
			if editErr == nil && action == "bind" {
				candidate, editErr = binding.apply(candidate, r.PostForm.Get("bindingId"))
			}
			if action == "add-account" {
				view.NewAccount = manualAccountFields{
					AppID:         strings.TrimSpace(r.PostForm.Get("newAppId")),
					AppSecretFile: strings.TrimSpace(r.PostForm.Get("newAppSecretFile")),
					StateDir:      strings.TrimSpace(r.PostForm.Get("newStateDir")),
					User:          strings.TrimSpace(r.PostForm.Get("newUser")),
					Tenant:        r.PostForm.Get("newTenant"),
					Route:         r.PostForm.Get("newRoute"),
				}
				if editErr == nil {
					candidate, editErr = view.NewAccount.apply(candidate)
					if editErr == nil {
						view.NewAccount = manualAccountFields{}
					}
				}
			}
			if replaceAppAction {
				fields := manualReplacementFields{
					ReplaceAppID:  r.PostForm.Get("replaceAppId"),
					AppID:         strings.TrimSpace(r.PostForm.Get("replacementAppId")),
					AppSecretFile: strings.TrimSpace(r.PostForm.Get("replacementAppSecretFile")),
					User:          strings.TrimSpace(r.PostForm.Get("replacementUser")),
					Tenant:        r.PostForm.Get("replacementTenant"),
				}
				if editErr == nil {
					var updated []byte
					if replaceSecret {
						updated, editErr = fields.applyWithSecret(candidate, source.Path, r.PostForm.Get("replacementSecret"))
					} else {
						updated, editErr = fields.apply(candidate, source.Path)
					}
					if editErr == nil {
						candidate = updated
					}
				}
				if editErr != nil {
					view.Replacement = &fields
				}
			}
			if editErr == nil && action == "import-legacy" {
				imported, err := view.LegacyImport.apply(candidate)
				editErr = err
				if editErr == nil {
					candidate = imported
					view.LegacyImport = nil
				}
			}
			view.Changes = configChanges(string(source.JSON), string(candidate))
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		view.JSON = string(candidate)
		parsed, err := bridge.ParseConfig(candidate)
		if editErr != nil {
			err = editErr
		}
		view.Valid = err == nil
		view.ShowFields = parsed.Routes != nil
		view.RawOpen = r.PostForm.Get("rawOpen") == "true" || !view.ShowFields
		if view.ShowFields {
			view.Accounts = webAccounts(parsed, candidate)
		}
		status := http.StatusOK
		switch {
		case err != nil:
			view.Error = err.Error()
			if action == "bind" {
				view.BindingID = r.PostForm.Get("bindingId")
			}
			if r.Method == http.MethodPost {
				status = http.StatusUnprocessableEntity
			}
		case action == "register-draft-project":
			status, view.Error = draftProject.start(w, r, source, view.Revision, candidate, parsed,
				r.PostForm.Get("projectAppId"), r.PostForm.Get("projectRouteId"))
			if status == http.StatusOK {
				return
			}
		case r.Method == http.MethodPost:
			view.Reviewed = configRevision(candidate)
			if action == "download" || action == "save" {
				switch {
				case r.PostForm.Get("reviewed") != view.Reviewed:
					view.Error = "候选配置已修改。请先确认本次差异，再下载或保存。"
					status = http.StatusConflict
				case action == "download":
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Disposition", `attachment; filename="t3-bridge.candidate.json"`)
					_, _ = w.Write(candidate)
					return
				case source.Path == "":
					view.Error = "使用 -config 指定本机保存路径，或下载候选 JSON。"
					status = http.StatusBadRequest
				default:
					if _, err := saveConfigCandidate(source.Path, candidate, view.Revision); err != nil {
						view.Error = err.Error()
						status = http.StatusConflict
					} else {
						http.Redirect(w, r, "/?source="+source.ID+"&saved=1", http.StatusSeeOther)
						return
					}
				}
			}
		}
		// Sensitive form submissions require a non-opaque Origin.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	})

	// Binding to loopback limits network access. The Host check also rejects
	// browser requests routed to loopback under a different hostname.
	sameOrigin := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		sameOrigin.ServeHTTP(w, r)
	})
}

func (fields manualAccountFields) apply(candidate []byte) ([]byte, error) {
	if fields.AppID == "" || fields.User == "" || !filepath.IsAbs(fields.AppSecretFile) || !filepath.IsAbs(fields.StateDir) {
		return candidate, errors.New("请填写应用 ID、初始用户 Open ID、密钥文件和独立状态目录的绝对路径")
	}
	if fields.Tenant != "feishu" && fields.Tenant != "lark" {
		return candidate, errors.New("请选择飞书或 Lark")
	}
	config, err := bridge.ParseConfig(candidate)
	if err != nil {
		return candidate, err
	}
	return addAccountDraft(candidate, config, bridge.Config{
		AppID: fields.AppID, AppSecretFile: fields.AppSecretFile, StateDir: fields.StateDir,
		Tenant: fields.Tenant, Users: []string{fields.User},
	}, fields.Route)
}

func webAccounts(parsed bridge.Config, raw []byte) []accountFields {
	accounts := []accountFields{webAccount(parsed, raw, 0)}
	var fields struct {
		Accounts []json.RawMessage `json:"additionalAccounts"`
	}
	_ = json.Unmarshal(raw, &fields)
	for i, account := range parsed.AdditionalAccounts {
		accounts = append(accounts, webAccount(account, fields.Accounts[i], i+1))
	}
	return accounts
}

func webAccount(config bridge.Config, raw []byte, number int) accountFields {
	var fields struct {
		InitialRuntime json.RawMessage `json:"initialRuntime"`
		Routes         map[string]struct {
			Options json.RawMessage `json:"options"`
		} `json:"routes"`
	}
	// The candidate has already been checked against the typed configuration.
	_ = json.Unmarshal(raw, &fields)
	var initialRuntime bytes.Buffer
	_ = json.Indent(&initialRuntime, fields.InitialRuntime, "", "  ")
	routes := make(map[string]routeFields, len(config.Routes))
	for id, target := range config.Routes {
		var formatted bytes.Buffer
		_ = json.Indent(&formatted, fields.Routes[id].Options, "", "  ")
		route := routeFields{Target: target, OptionsJSON: formatted.String(), ToolBinding: toolBindingFields(target.ToolContext, config.AppID, config.Tenant)}
		if target.ToolContext != nil {
			toolContext, _ := json.MarshalIndent(target.ToolContext, "", "  ")
			route.ToolContextJSON = string(toolContext)
		}
		routes[id] = route
	}
	fieldsForAccount := accountFields{Config: config, Number: number, InitialRuntimeJSON: initialRuntime.String(), RouteFields: routes}
	if number > 0 {
		fieldsForAccount.Index = strconv.Itoa(number - 1)
	}
	return fieldsForAccount
}
