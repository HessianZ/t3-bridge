package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func runConfigTUI(args []string) error {
	flags := flag.NewFlagSet("t3-bridge config tui", flag.ContinueOnError)
	path := flags.String("config", "", "open or create a local JSON file")
	profile := flags.String("profile", "", "use the named configuration profile")
	template := flags.String("template", "generic", "starting template: generic, multi-host, homeops")
	legacyBridge := flags.String("legacy-bridge", "", "absolute path to the old executable (default: discover on PATH)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	legacyProvider, err := legacyBridgeExecutable(*legacyBridge)
	if err != nil {
		if *legacyBridge != "" {
			return err
		}
		legacyProvider = ""
	}
	resolved, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	sources, err := configWebSources("")
	if err != nil {
		return err
	}
	for _, source := range sources {
		if source.ID != *template {
			continue
		}
		fallback := source
		if resolved != "" {
			absolute, err := filepath.Abs(resolved)
			if err != nil {
				return err
			}
			source, err = configFileSource(absolute, source.JSON)
			if err != nil {
				return err
			}
		}
		m := newConfigTUI(source)
		m.legacyProvider = legacyProvider
		m.profiles.registry, _ = localProfiles()
		m.profiles.fallback = fallback
		m.languageChoice = m.profiles.registry.workspaceLanguage()
		m.language = m.languageChoice
		if m.language == workspaceAuto {
			m.language = systemWorkspaceLanguage()
		}
		defer func() {
			if m.legacyImport.cancel != nil {
				m.legacyImport.cancel()
			}
			if m.authorization.cancel != nil {
				m.authorization.cancel()
			}
			m.closeFeishuBinding()
			if m.groups.cancel != nil {
				m.groups.cancel()
			}
			if m.liveGroups.cancel != nil {
				m.liveGroups.cancel()
			}
			if m.liveSettings.cancel != nil {
				m.liveSettings.cancel()
			}
			if m.liveAccess.cancel != nil {
				m.liveAccess.cancel()
			}
			if m.liveMeetings.cancel != nil {
				m.liveMeetings.cancel()
			}
			m.closeUserGroups()
			if m.toolIdentity.cancel != nil {
				m.toolIdentity.cancel()
			}
		}()
		_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
		return err
	}
	return fmt.Errorf("unknown config template %q; use generic, multi-host or homeops", *template)
}

type configTUIScreen int

const (
	tuiBrowse configTUIScreen = iota
	tuiEdit
	tuiReview
	tuiExport
	tuiAddRoute
	tuiAddAccount
	tuiImportLegacy
	tuiLegacyCopyConfirm
	tuiLegacyCopyPending
	tuiWorkspaceImportPath
	tuiWorkspaceImportMapping
	tuiWorkspaceImportAliases
	tuiWorkspaceImportEntry
	tuiWorkspaceImportReview
	tuiLegacySessionForm
	tuiLegacySessionInspecting
	tuiLegacySessionPick
	tuiLegacySessionLoading
	tuiLegacySessionReview
	tuiLegacySessionApplying
	tuiProfiles
	tuiCreateProfile
	tuiConfirmProfileOpen
	tuiConfirmProfileArchive
	tuiProfileExportPath
	tuiProfilePreviewLoading
	tuiConfirmProfilePurge
	tuiConfirmProfileExport
	tuiProfileApplying
	tuiProfileBundlePath
	tuiProfileBundleName
	tuiProfileBundlePatch
	tuiProfileBundleReading
	tuiProfileBundleReview
	tuiProfileBundleApplying
	tuiCatalogQuery
	tuiCatalogLoading
	tuiCatalogPick
	tuiAuthorizeReview
	tuiAuthorizePending
	tuiBindReview
	tuiBindProgress
	tuiBindManual
	tuiGroupsLoading
	tuiGroupsPick
	tuiGroupsFilter
	tuiLiveGroupsLoading
	tuiLiveGroupsPick
	tuiLiveGroupsFilter
	tuiLiveGroupsConfirm
	tuiUserGroupsLoading
	tuiUserGroupsPick
	tuiUserGroupsSearch
	tuiUserGroupsAuthorize
	tuiUserGroupsConfirm
	tuiLiveSettingsLoading
	tuiLiveSettingsPick
	tuiLiveSettingsEdit
	tuiLiveSettingsConfirm
	tuiLiveAccessLoading
	tuiLiveAccessPick
	tuiLiveAccessScope
	tuiLiveAccessUser
	tuiLiveAccessEdit
	tuiLiveAccessConfirm
	tuiServiceName
	tuiServiceConfirm
	tuiServicePreflightLoading
	tuiServicePreflightResult
	tuiServiceInstalling
	tuiServiceInstalled
	tuiServiceStarting
	tuiServiceManageLoading
	tuiServiceManagePick
	tuiServiceManageConfirm
	tuiServiceManageApplying
	tuiServiceManageLogs
	tuiCredentialsLoading
	tuiCredentialsPick
	tuiCredentialsInput
	tuiCredentialSetConfirm
	tuiCredentialRemoveConfirm
	tuiMessageReceiptInput
	tuiMessageReceiptLoading
	tuiMessageReceiptResult
	tuiDrainLoading
	tuiDrainResult
	tuiToolIdentityLoading
	tuiToolIdentityResult
	tuiToolBinding
	tuiProjectRegisterInput
	tuiProjectRegisterConfirm
	tuiProjectRegisterPending
	tuiReceiverEnableLoading
	tuiLiveReconnectLoading
	tuiLiveReconnectConfirm
	tuiLiveReconnectApplying
	tuiReceiverStatusLoading
	tuiReceiverStatusResult
	tuiLiveMeetingsLoading
	tuiLiveMeetingsPick
	tuiLiveMeetingsJoin
	tuiLiveMeetingsConfirm
	tuiLiveMeetingsApplying
	tuiMeetingPermissionsLoading
	tuiMeetingPermissionsResult
	tuiReceiverReadinessLoading
	tuiReceiverReadinessResult
	tuiTelemetryArchiveDate
	tuiTelemetryArchiveLoading
	tuiTelemetryArchiveResult
)

type configTUI struct {
	source                   webSource
	draft                    string
	language, languageChoice workspaceLanguage

	screen          configTUIScreen
	account, row    int
	route           string
	accounts        []configTUIObject
	routes          configTUIObject
	rows            []configTUIField
	editField       configTUIField
	editor          textarea.Model
	editStart       string
	review          viewport.Model
	reviewed        []byte
	input           textinput.Model
	newAccount      configTUIAccountForm
	legacyImport    configTUILegacyImportForm
	legacyProvider  string
	copyLegacy      func(legacyCredentialPlan, context.Context, string, []byte) error
	workspaceImport configTUIWorkspaceImport
	legacySession   configTUILegacySession
	profiles        configTUIProfiles
	catalog         configTUICatalog
	authorization   configTUIAuthorization
	binding         configTUIBinding
	groups          configTUIFeishuGroups
	liveGroups      configTUILiveGroups
	userGroups      configTUIUserGroups
	liveSettings    configTUILiveSettings
	liveAccess      configTUILiveAccess
	liveMeetings    configTUILiveMeetings
	liveReconnect   configTUILiveReconnect
	receiverStatus  configTUIReceiverStatus
	readiness       configTUIReceiverReadiness
	telemetry       configTUITelemetryArchive
	service         configTUIService
	serviceManage   configTUIServiceManage
	credentials     configTUICredentials
	messageReceipt  configTUIMessageReceipt
	drain           configTUIDrain
	toolIdentity    configTUIToolIdentity
	toolBinding     configTUIToolBindingForm
	projectRegister configTUIProjectRegister
	prepareReceiver func(context.Context, string, string, string) (receiverEnableDraft, error)
	width, height   int
	status          string
	confirmQuit     bool
}

func newConfigTUI(source webSource) *configTUI {
	m := &configTUI{
		review: viewport.New(80, 17), width: 80, height: 24,
		copyLegacy: legacyCredentialPlan.copy,
		service: configTUIService{
			install: installWorkspaceService, run: runHostService,
			preflight: checkSavedReceiverConfig, preflightApp: checkSavedReceiverAppConfig,
		},
		serviceManage:   configTUIServiceManage{inventory: readHostServiceInventory, readLogs: readLocalServiceLogs},
		receiverStatus:  configTUIReceiverStatus{lookup: receiverStatusForAccount},
		readiness:       configTUIReceiverReadiness{lookup: savedReceiverReadiness},
		telemetry:       configTUITelemetryArchive{lookup: lookupWorkspaceTelemetryArchive},
		messageReceipt:  configTUIMessageReceipt{lookup: lookupWorkspaceReceipt},
		drain:           configTUIDrain{check: checkSavedDrain},
		toolIdentity:    configTUIToolIdentity{check: checkSavedToolIdentity},
		projectRegister: configTUIProjectRegister{register: registerUnsavedProject, workspace: managedProjectWorkspace},
		prepareReceiver: prepareReceiverEnable,
		liveMeetings:    configTUILiveMeetings{checkPermissions: checkSavedMeetingPermissions, checkAvailability: checkSavedMeetingAvailability},
	}
	m.review.SetHorizontalStep(8)
	m.input = textinput.New()
	m.input.Prompt = ""
	m.input.Width = m.width
	m.loadSource(source)
	return m
}

func (m *configTUI) loadSource(source webSource) {
	m.source, m.draft, m.reviewed = source, string(source.JSON), nil
	m.account, m.row, m.route, m.status = 0, 0, "", ""
	m.refreshFields()
	if _, err := bridge.ParseConfig(source.JSON); err != nil {
		m.status = err.Error()
	}
}

func (m *configTUI) Init() tea.Cmd { return nil }

func (m *configTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.review.Width, m.review.Height = m.width, m.bodyHeight()
		m.review.SetYOffset(m.review.YOffset)
		if m.screen == tuiLegacyCopyConfirm {
			m.resizeLegacyCopyReview()
		}
		if m.screen == tuiConfirmProfilePurge || m.screen == tuiConfirmProfileExport {
			m.resizeProfileLifecycleReview()
		}
		if m.screen == tuiProfileBundleReview {
			m.resizeProfileBundleReview()
		}
		if (m.screen == tuiEdit && !m.editField.text) || m.screen == tuiWorkspaceImportMapping {
			m.editor.SetWidth(m.width)
			m.editor.SetHeight(m.bodyHeight())
		}
		m.input.Width = m.width
		m.credentials.input.Width = m.width
		m.binding.manual.secretInput.Width = m.width
	case tea.KeyMsg:
		if !msg.Paste {
			if cmd, handled := m.handleKey(msg.String()); handled {
				return m, cmd
			}
		}
	case configTUICatalogResult:
		m.receiveCatalog(msg)
		return m, nil
	case configTUILegacyCopyResult:
		m.receiveLegacyCopy(msg)
		return m, nil
	case configTUILegacySessionInspected:
		return m, m.receiveLegacySessionInspection(msg)
	case configTUILegacySessionPreview:
		return m, m.receiveLegacySessionPreview(msg)
	case configTUILegacySessionApplied:
		return m, m.receiveLegacySessionApplied(msg)
	case configTUIAuthorizationResult:
		return m, m.receiveAuthorization(msg)
	case configTUIBindingResult:
		return m, m.receiveFeishuBinding(msg)
	case configTUIFeishuGroupsResult:
		m.receiveFeishuGroups(msg)
		return m, nil
	case configTUILiveGroupsResult:
		m.receiveLiveGroups(msg)
		return m, nil
	case configTUILiveSettingsResult:
		m.receiveLiveSettings(msg)
		return m, nil
	case configTUILiveAccessResult:
		m.receiveLiveAccess(msg)
		return m, nil
	case configTUILiveMeetingsResult:
		m.receiveLiveMeetings(msg)
		return m, nil
	case configTUIMeetingPermissionsResult:
		m.receiveMeetingPermissions(msg)
		return m, nil
	case configTUILiveReconnectResult:
		m.receiveLiveReconnect(msg)
		return m, nil
	case configTUIReceiverStatusResult:
		m.receiveReceiverStatus(msg)
		return m, nil
	case configTUIReceiverReadinessResult:
		m.receiveReceiverReadiness(msg)
		return m, nil
	case configTUITelemetryArchiveResult:
		m.receiveTelemetryArchive(msg)
		return m, nil
	case configTUIUserGroupsResult:
		return m, m.receiveUserGroups(msg)
	case configTUIServiceResult:
		m.receiveServiceResult(msg)
		return m, nil
	case configTUIServicePreflight:
		m.receiveServicePreflight(msg)
		return m, nil
	case configTUIServiceManageInventory:
		m.receiveServiceManageInventory(msg)
		return m, nil
	case configTUIServiceManageResult:
		return m, m.receiveServiceManageResult(msg)
	case configTUIServiceManageLogResult:
		m.receiveServiceManageLogs(msg)
		return m, nil
	case configTUICredentialsResult:
		m.receiveCredentials(msg)
		return m, nil
	case configTUIMessageReceiptResult:
		m.receiveMessageReceipt(msg)
		return m, nil
	case configTUIDrainResult:
		m.receiveDrain(msg)
		return m, nil
	case configTUIToolIdentityResult:
		m.receiveToolIdentity(msg)
		return m, nil
	case configTUIProjectRegisterResult:
		m.receiveProjectRegistration(msg)
		return m, nil
	case configTUIReceiverEnableResult:
		m.receiveReceiverEnable(msg)
		return m, nil
	case configTUIProfilePreview:
		return m, m.receiveProfileLifecyclePreview(msg)
	case configTUIProfileApplied:
		m.receiveProfileLifecycleApplied(msg)
		return m, nil
	case configTUIProfileBundlePreview:
		return m, m.receiveProfileBundlePreview(msg)
	case configTUIProfileBundleApplied:
		return m, m.receiveProfileBundleApplied(msg)
	}

	var cmd tea.Cmd
	switch m.screen {
	case tuiEdit:
		if m.editField.text {
			m.input, cmd = m.input.Update(msg)
		} else {
			m.editor, cmd = m.editor.Update(msg)
		}
	case tuiWorkspaceImportMapping:
		m.editor, cmd = m.editor.Update(msg)
	case tuiReview, tuiWorkspaceImportReview, tuiLegacySessionReview, tuiProfileBundleReview, tuiServicePreflightResult, tuiServiceManageLogs, tuiMessageReceiptResult, tuiDrainResult, tuiToolIdentityResult, tuiProjectRegisterConfirm,
		tuiReceiverStatusLoading, tuiReceiverStatusResult, tuiMeetingPermissionsResult, tuiReceiverReadinessResult, tuiTelemetryArchiveResult:
		m.review, cmd = m.review.Update(msg)
	case tuiLegacyCopyConfirm:
		m.legacyImport.confirmation, cmd = m.legacyImport.confirmation.Update(msg)
	case tuiBindManual:
		cmd = m.updateManualBindingInput(msg)
	case tuiExport, tuiAddRoute, tuiAddAccount, tuiImportLegacy, tuiWorkspaceImportPath, tuiWorkspaceImportEntry, tuiLegacySessionForm, tuiCreateProfile,
		tuiProfileExportPath, tuiConfirmProfilePurge, tuiProfileBundlePath, tuiProfileBundleName, tuiProfileBundlePatch, tuiCatalogQuery,
		tuiGroupsFilter, tuiLiveGroupsFilter, tuiUserGroupsSearch, tuiLiveAccessUser, tuiServiceName, tuiMessageReceiptInput, tuiProjectRegisterInput, tuiLiveMeetingsJoin, tuiTelemetryArchiveDate:
		m.input, cmd = m.input.Update(msg)
	case tuiLiveSettingsEdit:
		if m.liveSettings.fields[m.liveSettings.row].Max > 0 {
			m.input, cmd = m.input.Update(msg)
		}
	case tuiToolBinding:
		if m.toolBinding.row != 0 {
			m.input, cmd = m.input.Update(msg)
		}
	case tuiCredentialsInput:
		if key, ok := msg.(tea.KeyMsg); ok {
			m.updateCredentialInput(key)
		}
	}
	return m, cmd
}

func (m *configTUI) handleKey(key string) (tea.Cmd, bool) {
	if m.screen == tuiBrowse && key == "f3" {
		next := workspaceEN
		switch m.languageChoice {
		case workspaceEN:
			next = workspaceZH
		case workspaceZH:
			next = workspaceAuto
		}
		if m.profiles.registry.root == "" || m.profiles.registry.saveWorkspaceLanguage(next) != nil {
			m.status = "无法保存语言设置。"
			return nil, true
		}
		m.languageChoice, m.language = next, next
		if next == workspaceAuto {
			m.language = systemWorkspaceLanguage()
		}
		return nil, true
	}
	if m.screen == tuiProjectRegisterPending {
		if key == "ctrl+c" || key == "esc" {
			m.status = "请求已提交，项目可能已登记。请等待结果。"
		}
		return nil, true
	}
	if m.screen == tuiLiveReconnectApplying || m.screen == tuiLiveMeetingsApplying {
		return nil, true
	}
	if m.screen == tuiProfileBundleReading || m.screen == tuiProfileBundleApplying || m.screen == tuiLegacySessionApplying {
		if key == "ctrl+c" || key == "esc" {
			m.status = "请等待当前文件操作完成"
		}
		return nil, true
	}
	if key == "ctrl+c" && m.screen == tuiProfileApplying {
		m.status = "请等待当前文件操作完成"
		return nil, true
	}
	if key == "ctrl+c" || (key == "q" && (m.screen == tuiBrowse || m.screen == tuiReview)) {
		switch m.screen {
		case tuiCredentialsInput, tuiCredentialSetConfirm, tuiCredentialRemoveConfirm:
			m.cancelCredentialEdit()
		case tuiBindManual:
			if m.binding.manual.enterSecret {
				m.binding.manual.secretInput.Reset()
			}
		}
		editing := m.screen == tuiAddAccount || m.screen == tuiBindManual || m.screen == tuiImportLegacy || m.screen == tuiToolBinding || (m.screen == tuiEdit && m.editorValue() != m.editStart)
		if m.confirmQuit || (!editing && m.draft == string(m.source.JSON)) {
			if m.userGroups.cancel != nil {
				m.userGroups.cancel()
			}
			return tea.Quit, true
		}
		m.confirmQuit = true
		m.status = "再次按退出键丢弃未保存的草稿"
		return nil, true
	}
	if m.confirmQuit {
		m.confirmQuit, m.status = false, ""
	}
	switch m.screen {
	case tuiBrowse:
		switch key {
		case "up", "k":
			m.row = max(0, m.row-1)
		case "down", "j":
			m.row = min(len(m.rows)-1, m.row+1)
		case "home":
			m.row = 0
		case "end":
			m.row = len(m.rows) - 1
		case "tab", "shift+tab":
			delta := 1
			if key == "shift+tab" {
				delta = -1
			}
			m.account = (m.account + delta + len(m.accounts)) % len(m.accounts)
			m.route, m.row = "", 0
			m.refreshFields()
		case "enter":
			field := m.rows[m.row]
			if field.routeID == "" {
				return m.openEditor(field), true
			}
			m.route, m.row = field.routeID, 0
			m.refreshFields()
		case "esc":
			if m.route != "" {
				m.leaveRoute()
			}
		case "r":
			m.screen, m.status = tuiAddRoute, ""
			return m.openInput(""), true
		case "a":
			return m.openAccountForm(), true
		case "L":
			return m.openLegacyImport(), true
		case "W":
			return m.openWorkspaceImport(), true
		case "S":
			return m.openLegacySession(), true
		case "d":
			m.removeRoute()
		case "D":
			m.removeAccount()
		case "J":
			return m.openEditor(configTUIField{label: "完整 JSON"}), true
		case "C":
			return m.openCredentials(), true
		case "I":
			return m.openServiceInstall(), true
		case "P":
			return m.openServicePreflight(key), true
		case "A":
			if m.route != "" {
				return nil, false
			}
			return m.openServicePreflight(key), true
		case "E":
			if m.route != "" {
				return nil, false
			}
			return m.openReceiverEnable(), true
		case "ctrl+r":
			if m.route != "" {
				return nil, false
			}
			if m.profiles.registry.root == "" {
				m.status = "当前用户的接收进程清单不可用"
				return nil, true
			}
			profiles := m.profiles.registry
			return m.openLiveReconnect(func() ([]receiverProcess, error) { return readRuntimeReceivers(profiles) }, localReceiverProcesses(profiles).reconnect), true
		case "ctrl+d":
			if m.route != "" {
				return nil, false
			}
			return m.openReceiverStatus(), true
		case "ctrl+p":
			return m.requestReceiverReadiness(), true
		case "Y":
			if m.route != "" {
				return nil, false
			}
			return m.openTelemetryArchive(), true
		case "H":
			if m.route != "" {
				return nil, false
			}
			profiles := m.profiles.registry
			return m.openLiveMeetings(func(account receiverProcessAccount) (receiverProcess, error) {
				return findLiveMeetingProcess(profiles, account)
			}, localReceiverProcesses(profiles).meeting), true
		case "M":
			return m.openServiceManage(), true
		case "G":
			return m.openDrain(), true
		case "v":
			return m.openMessageReceipt(), true
		case "V":
			return m.openToolIdentity(), true
		case "B":
			return m.openToolBinding(), true
		case "K":
			return m.openProjectRegister(), true
		case "T":
			if m.route != "" {
				return nil, false
			}
			candidate, err := useLocalT3([]byte(m.draft), m.account)
			if err != nil {
				m.status = err.Error()
				break
			}
			m.setDraft(candidate)
			m.preview()
		case "p":
			m.preview()
		case "f4":
			m.openProfiles()
		case "f5":
			if m.route != "" {
				m.screen, m.status = tuiCatalogQuery, ""
				return m.openInput(""), true
			}
			m.status = "先打开要配置的路由"
		case "f6":
			m.openAuthorization()
		case "f7":
			m.openFeishuBinding()
		case "f8":
			return m.queryFeishuGroups(bridge.ListFeishuGroups), true
		case "f9":
			if m.profiles.registry.root == "" {
				m.status = "当前用户的接收进程清单不可用"
				return nil, true
			}
			registry := localReceiverProcesses(m.profiles.registry)
			return m.openLiveGroups(registry.list, registry.groupAccess), true
		case "f10":
			return m.openUserGroups(), true
		case "f11":
			if m.profiles.registry.root == "" {
				m.status = "当前用户的接收进程清单不可用"
				return nil, true
			}
			registry := localReceiverProcesses(m.profiles.registry)
			return m.openLiveSettings(registry.list, registry.runSettings), true
		case "f12":
			if m.profiles.registry.root == "" {
				m.status = "当前用户的接收进程清单不可用"
				return nil, true
			}
			registry := localReceiverProcesses(m.profiles.registry)
			return m.openLiveAccess(registry.list, registry.accessPolicy), true
		case "b":
			backup, err := readConfigBackup(m.source.Path)
			if err != nil {
				m.status = err.Error()
			} else {
				m.previewDraft(backup)
			}
		default:
			return nil, false
		}
	case tuiEdit:
		switch key {
		case "ctrl+s":
			m.applyEdit()
		case "enter":
			if !m.editField.text {
				return nil, false
			}
			m.applyEdit()
		case "f2":
			if m.editField.text {
				m.input.Reset()
			} else {
				m.editor.Reset()
			}
		case "esc":
			m.editor.Blur()
			m.input.Blur()
			m.screen, m.status = tuiBrowse, ""
		default:
			return nil, false
		}
	case tuiReview:
		switch key {
		case "esc":
			m.legacyImport = configTUILegacyImportForm{}
			m.screen = tuiBrowse
		case "s":
			m.save()
			if m.screen == tuiBrowse {
				m.legacyImport = configTUILegacyImportForm{}
			}
		case "e":
			if m.reviewed != nil {
				m.screen, m.status = tuiExport, ""
				return m.openInput(""), true
			}
		case "c":
			m.openLegacyCopy()
		case "L":
			if m.legacyImport.matches(m.draft, m.reviewed) {
				return m.showLegacyImport(), true
			}
		default:
			return nil, false
		}
	case tuiExport, tuiAddRoute:
		switch key {
		case "esc":
			if m.screen == tuiExport {
				m.screen = tuiReview
			} else {
				m.screen = tuiBrowse
			}
			m.status = ""
			m.input.Blur()
		case "enter":
			if m.screen == tuiExport {
				m.export()
			} else {
				m.addRoute()
			}
		default:
			return nil, false
		}
	case tuiAddAccount:
		return m.accountFormKey(key)
	case tuiImportLegacy:
		return m.legacyImportKey(key)
	case tuiLegacyCopyConfirm:
		return m.legacyCopyKey(key)
	case tuiWorkspaceImportPath, tuiWorkspaceImportMapping, tuiWorkspaceImportAliases, tuiWorkspaceImportEntry, tuiWorkspaceImportReview:
		return m.workspaceImportKey(key)
	case tuiLegacySessionForm, tuiLegacySessionInspecting, tuiLegacySessionPick, tuiLegacySessionLoading, tuiLegacySessionReview:
		return m.legacySessionKey(key)
	case tuiProfiles, tuiCreateProfile, tuiConfirmProfileOpen, tuiConfirmProfileArchive:
		return m.profileKey(key)
	case tuiProfileExportPath, tuiProfilePreviewLoading, tuiConfirmProfilePurge, tuiConfirmProfileExport, tuiProfileApplying:
		return m.profileLifecycleKey(key)
	case tuiProfileBundlePath, tuiProfileBundleName, tuiProfileBundlePatch, tuiProfileBundleReview:
		return m.profileBundleImportKey(key)
	case tuiCatalogQuery, tuiCatalogLoading, tuiCatalogPick:
		return m.catalogKey(key)
	case tuiAuthorizeReview, tuiAuthorizePending:
		return m.authorizationKey(key)
	case tuiBindReview, tuiBindProgress:
		return m.feishuBindingKey(key)
	case tuiBindManual:
		return m.manualBindingKey(key)
	case tuiGroupsLoading, tuiGroupsPick:
		if m.screen == tuiGroupsPick && key == "/" {
			m.screen, m.status = tuiGroupsFilter, ""
			return m.openInput(m.groups.filter), true
		}
		return nil, m.feishuGroupsKey(key)
	case tuiGroupsFilter:
		switch key {
		case "enter":
			m.groups.filter = strings.TrimSpace(m.input.Value())
			m.groups.row = 0
		case "esc":
		default:
			return nil, false
		}
		m.input.Blur()
		m.screen = tuiGroupsPick
	case tuiLiveGroupsLoading, tuiLiveGroupsPick, tuiLiveGroupsFilter, tuiLiveGroupsConfirm:
		return m.liveGroupsKey(key)
	case tuiLiveSettingsLoading, tuiLiveSettingsPick, tuiLiveSettingsEdit, tuiLiveSettingsConfirm:
		return m.liveSettingsKey(key)
	case tuiLiveAccessLoading, tuiLiveAccessPick, tuiLiveAccessScope, tuiLiveAccessUser, tuiLiveAccessEdit, tuiLiveAccessConfirm:
		return m.liveAccessKey(key)
	case tuiLiveMeetingsLoading, tuiLiveMeetingsPick, tuiLiveMeetingsJoin, tuiLiveMeetingsConfirm, tuiMeetingPermissionsLoading, tuiMeetingPermissionsResult:
		return m.liveMeetingsKey(key)
	case tuiLiveReconnectLoading, tuiLiveReconnectConfirm:
		return m.liveReconnectKey(key)
	case tuiReceiverStatusLoading, tuiReceiverStatusResult:
		return m.receiverStatusKey(key)
	case tuiReceiverReadinessLoading, tuiReceiverReadinessResult:
		return m.receiverReadinessKey(key)
	case tuiTelemetryArchiveDate, tuiTelemetryArchiveLoading, tuiTelemetryArchiveResult:
		return m.telemetryArchiveKey(key)
	case tuiUserGroupsLoading, tuiUserGroupsPick, tuiUserGroupsSearch, tuiUserGroupsAuthorize, tuiUserGroupsConfirm:
		return m.userGroupsKey(key)
	case tuiServiceName, tuiServiceConfirm, tuiServicePreflightLoading, tuiServicePreflightResult, tuiServiceInstalling, tuiServiceInstalled, tuiServiceStarting:
		return m.serviceKey(key)
	case tuiServiceManageLoading, tuiServiceManagePick, tuiServiceManageConfirm, tuiServiceManageApplying, tuiServiceManageLogs:
		return m.serviceManageKey(key)
	case tuiCredentialsLoading, tuiCredentialsPick, tuiCredentialsInput, tuiCredentialSetConfirm, tuiCredentialRemoveConfirm:
		return m.credentialsKey(key)
	case tuiMessageReceiptInput, tuiMessageReceiptLoading, tuiMessageReceiptResult:
		return m.messageReceiptKey(key)
	case tuiDrainLoading, tuiDrainResult:
		return nil, m.drainKey(key)
	case tuiToolIdentityLoading, tuiToolIdentityResult:
		return nil, m.toolIdentityKey(key)
	case tuiToolBinding:
		return m.toolBindingKey(key)
	case tuiProjectRegisterInput, tuiProjectRegisterConfirm:
		return m.projectRegisterKey(key)
	}
	return nil, true
}

func (m *configTUI) bodyHeight() int {
	if m.screen == tuiBrowse {
		height := m.height - 9
		if m.route == "" {
			height--
		}
		if m.hasSavedReceiverReadinessSource() {
			height--
		}
		return max(1, height-len(m.firstRunGuidance()))
	}
	return max(1, m.height-7)
}

func (m *configTUI) firstRunGuidance() []string {
	if m.screen != tuiBrowse || m.account != 0 || m.route != "" || m.source.Path == "" || m.source.Revision != "missing" {
		return nil
	}
	lines := []string{
		m.language.text("首次配置 · F7 扫码或填 ID/密钥文件"),
		m.language.text("T 本机 T3 · F6 授权 · 进路由"),
		m.language.text("F5 选目标 · K 登记项目 · p 预览 · s 保存"),
		m.language.text("保存后 A 预检 · E 审阅启用后 s 保存"),
	}
	return lines[:min(len(lines), max(0, m.height-5))]
}

func (m *configTUI) openInput(value string) tea.Cmd {
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

func (m *configTUI) View() string {
	var title, body, shortcuts, actions, more, onlineActions, migrations, receiverActions, readinessActions string
	switch m.screen {
	case tuiBrowse:
		title = m.accountTitle()
		body = m.fieldList()
		shortcuts = m.language.text("F4 命名配置 · ↑↓ 选择 · Enter 编辑 · Tab / Shift+Tab 切换账号 · J 完整 JSON")
		actions = m.language.text("v 查询消息回执 · F6 T3 授权 · F7 飞书绑定 · a 新增账号 · r 新增路由 · d 移除路由")
		if m.route != "" {
			title += m.language.text(" / 路由 ") + m.route
			actions = m.language.text("B 编辑工具绑定 · V 核验工具身份 · F5 查询 T3 · Esc 返回 · ") + actions
		} else if m.account != 0 {
			actions += m.language.text(" · D 移除账号")
		}
		migrations = m.language.text("L 导入旧配置 · W 迁移工作区 · S 转接旧会话")
		if m.route == "" {
			migrations = m.language.text("H 在线会议 · ") + migrations + m.language.text(" · T 使用本机 T3")
		} else {
			migrations += m.language.text(" · K 登记 T3 项目")
		}
		more = m.language.text("G 排空检查 · I 安装服务 · M 管理实例 · p 预览")
		if m.route == "" {
			receiverActions = m.language.text("Ctrl+D 接收状态 · Ctrl+R 重连接收端")
		}
		if m.hasSavedReceiverReadinessSource() {
			readinessActions = m.language.text("Ctrl+P 全部 App 就绪")
			if m.route == "" {
				readinessActions += m.language.text(" · Y 每日遥测")
			}
		}
		if m.savedServiceConfigIssue() == "" {
			readinessActions += m.language.text(" · P 预检全部账号")
			if m.route == "" {
				receiverActions += m.language.text(" · A 预检所选 App · E 准备启用接收")
			}
		}
		if m.source.Path != "" && m.source.Revision != "missing" {
			more += m.language.text(" · b 备份")
			if m.route == "" && m.account == 0 {
				actions += m.language.text(" · C 凭据")
			}
		}
		more += m.language.text(" · q 退出")
		onlineActions = m.language.text("F8 选择飞书群 · F9 在线群权限 · F10 我的群 · F11 在线运行设置 · F12 在线用户权限")
	case tuiEdit:
		title = m.editField.labelIn(m.language)
		shortcuts = m.language.text("Ctrl+S 应用到草稿 · F2 清空 · Esc 返回")
		switch {
		case m.editField.text:
			body = m.input.View()
			shortcuts = m.language.text("Enter / Ctrl+S 应用到草稿 · F2 清空 · Esc 返回")
			actions = m.language.text("直接输入文本。清空内容可删除字段。")
		case len(m.editField.path) != 0:
			body = m.editor.View()
			actions = m.language.text("填写 JSON 值。清空内容可删除字段。")
			if m.editField.path[len(m.editField.path)-1] == "toolContext" {
				actions = m.language.text("身份可选 bot、user 或 user-default。双身份使用同一 App 和 CLI profile。")
			}
		default:
			body = m.editor.View()
		}
	case tuiReview:
		title = m.language.text("候选配置差异")
		body = m.review.View()
		shortcuts = m.language.text("↑↓←→ 滚动 · Esc 返回 · q 退出")
		if m.reviewed != nil {
			actions = m.language.text("e 导出到新文件")
			if m.source.Path != "" {
				actions = m.language.text("s 保存 · ") + actions
			}
		}
		if m.legacyImport.candidate != "" {
			more = m.language.text("c 复制 App Secret · L 返回导入表单")
		}
	case tuiExport:
		title = m.language.text("导出候选配置")
		body = m.input.View()
		shortcuts = m.language.text("Enter 导出 · Esc 返回")
		actions = m.language.text("输入新 .json 文件的路径")
	case tuiAddRoute:
		title = m.accountTitle() + m.language.text(" / 新增路由")
		body = m.input.View()
		shortcuts = m.language.text("Enter 添加到草稿 · Esc 返回")
		actions = m.language.text("输入路由 ID。添加后填写目标和路由用户。")
	case tuiAddAccount:
		title = m.language.text("新增附加账号")
		body = m.accountFormView()
		shortcuts = m.language.text("↑↓ / Tab 选字段 · ←→ 选租户和主路由")
		actions = m.language.text("Enter 下一项 · Ctrl+S 添加到草稿 · Esc 返回")
	case tuiImportLegacy:
		title, body, shortcuts, actions = m.legacyImportView()
	case tuiLegacyCopyConfirm:
		title = m.language.text("确认复制 App Secret")
		body = m.legacyImport.confirmation.View()
		shortcuts = m.language.text("y 确认 · Esc 取消 · ↑↓ 滚动")
	case tuiLegacyCopyPending:
		title = m.language.text("复制 App Secret")
		body = m.language.text("正在读取旧桥凭据并写入私有文件…")
	case tuiWorkspaceImportPath, tuiWorkspaceImportMapping, tuiWorkspaceImportAliases, tuiWorkspaceImportEntry, tuiWorkspaceImportReview:
		title, body, shortcuts, actions = m.workspaceImportView()
	case tuiLegacySessionForm, tuiLegacySessionInspecting, tuiLegacySessionPick, tuiLegacySessionLoading, tuiLegacySessionReview, tuiLegacySessionApplying:
		title, body, shortcuts, actions = m.legacySessionView()
	case tuiProfiles, tuiCreateProfile, tuiConfirmProfileOpen, tuiConfirmProfileArchive:
		title, body, shortcuts, actions = m.profileView()
	case tuiProfileExportPath, tuiProfilePreviewLoading, tuiConfirmProfilePurge, tuiConfirmProfileExport, tuiProfileApplying:
		title, body, shortcuts, actions = m.profileLifecycleView()
	case tuiProfileBundlePath, tuiProfileBundleName, tuiProfileBundlePatch, tuiProfileBundleReading, tuiProfileBundleReview, tuiProfileBundleApplying:
		title, body, shortcuts, actions = m.profileBundleImportView()
	case tuiCatalogQuery, tuiCatalogLoading, tuiCatalogPick:
		title, body, shortcuts, actions = m.catalogView()
	case tuiAuthorizeReview, tuiAuthorizePending:
		title, body, shortcuts = m.authorizationView()
	case tuiBindReview, tuiBindProgress:
		title, body, shortcuts = m.feishuBindingView()
		if m.screen == tuiBindReview {
			lines := strings.Split(body, "\n")
			body = strings.Join(lines[:min(len(lines), m.bodyHeight())], "\n")
			actions = m.language.text("m 手动替换所选应用")
		}
	case tuiBindManual:
		title = m.language.text("手动换绑 · ") + m.binding.manual.fields.ReplaceAppID
		body = m.manualBindingView()
		shortcuts = m.language.text("F2 直接输入 App Secret · ↑↓ / Tab 选字段 · ←→ 选租户")
		actions = m.language.text("Enter 下一项 · Ctrl+S 审阅草稿 · Esc 返回")
		if m.binding.manual.enterSecret {
			shortcuts = m.language.text("F2 使用已有文件 · ↑↓ / Tab 选字段 · ←→ 选租户")
			actions = m.language.text("Ctrl+S 写入新的私有 App Secret 文件并审阅差异 · Esc 返回")
		}
	case tuiGroupsLoading, tuiGroupsPick:
		title, body, shortcuts, actions = m.feishuGroupsView()
	case tuiGroupsFilter:
		title = m.accountTitle() + m.language.text(" / 搜索机器人所在群")
		body = m.input.View()
		shortcuts = m.language.text("输入群名或 ID · Enter 搜索 · 清空后 Enter 显示全部 · Esc 返回")
	case tuiLiveGroupsLoading, tuiLiveGroupsPick, tuiLiveGroupsFilter, tuiLiveGroupsConfirm:
		title, body, shortcuts, actions = m.liveGroupsView()
	case tuiLiveSettingsLoading, tuiLiveSettingsPick, tuiLiveSettingsEdit, tuiLiveSettingsConfirm:
		title, body, shortcuts, actions = m.liveSettingsView()
	case tuiLiveAccessLoading, tuiLiveAccessPick, tuiLiveAccessScope, tuiLiveAccessUser, tuiLiveAccessEdit, tuiLiveAccessConfirm:
		title, body, shortcuts, actions = m.liveAccessView()
	case tuiLiveMeetingsLoading, tuiLiveMeetingsPick, tuiLiveMeetingsJoin, tuiLiveMeetingsConfirm, tuiLiveMeetingsApplying:
		title, body, shortcuts, actions = m.liveMeetingsView()
	case tuiMeetingPermissionsLoading, tuiMeetingPermissionsResult:
		title, body, shortcuts, actions = m.meetingPermissionsView()
	case tuiLiveReconnectLoading, tuiLiveReconnectConfirm, tuiLiveReconnectApplying:
		title, body, shortcuts = m.liveReconnectView()
	case tuiReceiverStatusLoading, tuiReceiverStatusResult:
		title, body, shortcuts = m.language.text("接收状态"), m.review.View(), m.language.text("r 刷新 · Esc 返回 · ↑↓←→ 滚动")
	case tuiReceiverReadinessLoading, tuiReceiverReadinessResult:
		title, body, shortcuts = m.language.text("接收就绪 · 全部 App"), m.review.View(), m.language.text("r 刷新 · Esc 返回 · ↑↓←→ 滚动")
		actions = m.language.text("只读 · 已保存配置")
	case tuiTelemetryArchiveDate, tuiTelemetryArchiveLoading, tuiTelemetryArchiveResult:
		title, body, shortcuts, actions, more = m.telemetryArchiveView()
	case tuiUserGroupsLoading, tuiUserGroupsPick, tuiUserGroupsSearch, tuiUserGroupsAuthorize, tuiUserGroupsConfirm:
		title, body, shortcuts, actions = m.userGroupsView()
	case tuiServiceName, tuiServiceConfirm, tuiServicePreflightLoading, tuiServicePreflightResult, tuiServiceInstalling, tuiServiceInstalled, tuiServiceStarting:
		title, body, shortcuts = m.serviceView()
	case tuiServiceManageLoading, tuiServiceManagePick, tuiServiceManageConfirm, tuiServiceManageApplying, tuiServiceManageLogs:
		title, body, shortcuts, actions = m.serviceManageView()
	case tuiCredentialsLoading, tuiCredentialsPick, tuiCredentialsInput, tuiCredentialSetConfirm, tuiCredentialRemoveConfirm:
		title, body, shortcuts, actions = m.credentialsView()
	case tuiMessageReceiptInput, tuiMessageReceiptLoading, tuiMessageReceiptResult:
		title, body, shortcuts = m.messageReceiptView()
	case tuiDrainLoading, tuiDrainResult:
		title, body, shortcuts, actions = m.drainView()
	case tuiToolIdentityLoading, tuiToolIdentityResult:
		title, body, shortcuts, actions = m.toolIdentityView()
	case tuiToolBinding:
		title, body, shortcuts, actions = m.toolBindingView()
	case tuiProjectRegisterInput, tuiProjectRegisterConfirm, tuiProjectRegisterPending:
		title, body, shortcuts, actions = m.projectRegisterView()
	case tuiReceiverEnableLoading:
		title = m.language.text("正在预检所选应用…")
		body = ansi.Truncate(configTUIPlain(m.accountTitle()), m.width, "…")
	}
	header := m.language.text("T3 Bridge 配置")
	if m.draft != string(m.source.JSON) {
		header += m.language.text(" · 草稿已修改")
	}
	source := m.language.sourceLabel(m.source.Label)
	if m.source.Path != "" {
		source = m.source.Path
		if m.source.Revision == "missing" {
			source += m.language.text("（新文件）")
		}
	}
	line := func(value string) string {
		return ansi.Truncate(configTUIPlain(strings.ReplaceAll(value, "\n", " ")), m.width, "…")
	}
	if m.screen == tuiBrowse {
		choice := m.language.text("跟随系统")
		switch m.languageChoice {
		case workspaceEN:
			choice = "English"
		case workspaceZH:
			choice = "简体中文"
		}
		header += " · " + m.language.text("F3 语言") + " (" + choice + ")"
	}
	lines := []string{line(header), line(source), line(title)}
	guidance := m.firstRunGuidance()
	for _, hint := range guidance {
		lines = append(lines, line(hint))
	}
	lines = append(lines, body, line(m.language.text(m.status)), line(shortcuts), line(actions))
	if onlineActions != "" {
		lines = append(lines, line(onlineActions))
	}
	if migrations != "" {
		lines = append(lines, line(migrations))
	}
	if receiverActions != "" {
		lines = append(lines, line(receiverActions))
	}
	if readinessActions != "" {
		lines = append(lines, line(readinessActions))
	}
	lines = append(lines, line(more))
	if len(guidance) > 0 {
		// Keep the selected field and status visible when the footer cannot fit.
		lines = lines[:min(len(lines), m.height-strings.Count(body, "\n"))]
	}
	return strings.Join(lines, "\n")
}

func configTUIPlain(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return '�'
		}
		return r
	}, value)
}
