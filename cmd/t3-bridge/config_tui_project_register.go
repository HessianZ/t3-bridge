package main

import (
	"context"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIProjectRegister struct {
	register                       func(ctx context.Context, configPath, revision string, draft []byte, appID, routeID, directory, setupTokenFile string) ([]byte, error)
	workspace                      func(string, bridge.Config, string) (string, error)
	appID, environment, routeID    string
	draft                          string
	managedDirectory               string
	directoryDraft, setupTokenFile string
	editingSetupToken              bool
}

type configTUIProjectRegisterResult struct {
	candidate []byte
	err       error
}

func (m *configTUI) openProjectRegister() tea.Cmd {
	source := m.source
	switch {
	case m.route == "":
		m.status = "先打开要登记 T3 项目的路由"
		return nil
	case source.Path == "" || source.Revision == "":
		m.status = "请通过 -config 选择保存路径，再登记 T3 项目"
		return nil
	}
	config, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil {
		m.status = "请先修正当前草稿 · " + err.Error()
		return nil
	}
	account := config.Accounts()[m.account]
	f := &m.projectRegister
	m.clearProjectRegisterInput()
	f.appID, f.environment, f.routeID = account.AppID, account.Routes[m.route].EnvironmentID, m.route
	f.draft = m.draft
	f.managedDirectory = ""
	if directory, err := f.workspace(source.Path, account, m.route); err == nil {
		f.managedDirectory = directory
	}
	m.screen, m.status = tuiProjectRegisterInput, ""
	return m.openInput("")
}

func (m *configTUI) projectRegisterKey(key string) (tea.Cmd, bool) {
	if key == "esc" {
		m.clearProjectRegisterInput()
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	}
	f := &m.projectRegister
	if m.screen == tuiProjectRegisterInput {
		switch key {
		case "f2":
			m.input.Reset()
		case "f3":
			if !f.editingSetupToken {
				f.directoryDraft = m.input.Value()
				f.editingSetupToken, m.status = true, ""
				return m.openInput(f.setupTokenFile), true
			}
		case "enter":
			if f.editingSetupToken {
				f.setupTokenFile = m.input.Value()
				f.editingSetupToken = false
				return m.openInput(f.directoryDraft), true
			}
			directory := m.input.Value()
			if directory == "" && f.managedDirectory == "" {
				m.status = "请输入目标机上已存在目录的绝对路径"
				break
			}
			if directory == "" {
				directory = f.managedDirectory
			}
			m.input.Blur()
			m.screen, m.status = tuiProjectRegisterConfirm, ""
			m.review.Height = m.bodyHeight()
			oneLine := func(value string) string {
				return configTUIPlain(strings.ReplaceAll(value, "\n", " "))
			}
			authorization := "账号 MCP 凭据"
			if f.setupTokenFile != "" {
				authorization = oneLine(f.setupTokenFile)
			}
			m.review.SetContent("App ID · " + oneLine(f.appID) + "\nT3 环境 · " + oneLine(f.environment) +
				"\n路由 · " + oneLine(f.routeID) + "\n目录 · " + oneLine(directory) + "\n本次登记授权 · " + authorization)
			m.review.SetXOffset(0)
			m.review.GotoTop()
		default:
			return nil, false
		}
		return nil, true
	}
	if key != "y" {
		return nil, false
	}
	path, revision, directory := m.source.Path, m.source.Revision, m.input.Value()
	draft := []byte(f.draft)
	register, appID, routeID, setupTokenFile := f.register, f.appID, f.routeID, f.setupTokenFile
	m.clearProjectRegisterInput()
	m.screen, m.status = tuiProjectRegisterPending, ""
	return func() tea.Msg {
		candidate, err := register(context.Background(), path, revision, draft, appID, routeID, directory, setupTokenFile)
		return configTUIProjectRegisterResult{candidate: candidate, err: err}
	}, true
}

func (m *configTUI) clearProjectRegisterInput() {
	f := &m.projectRegister
	f.draft = ""
	f.directoryDraft, f.setupTokenFile, f.editingSetupToken = "", "", false
	m.input.Blur()
	m.input.Reset()
	m.review.SetContent("")
}

func (m *configTUI) receiveProjectRegistration(msg configTUIProjectRegisterResult) {
	if m.screen != tuiProjectRegisterPending {
		return
	}
	if msg.err != nil {
		m.screen, m.status = tuiBrowse, msg.err.Error()
		return
	}
	m.previewDraft(msg.candidate)
	if m.reviewed != nil {
		m.draft = string(m.reviewed)
		m.refreshFields()
		m.status = "T3 项目已登记 · 请核对差异并按 s 保存"
	}
}

func (m *configTUI) projectRegisterView() (title, body, shortcuts, actions string) {
	switch m.screen {
	case tuiProjectRegisterInput:
		input := ansi.Truncate(m.input.View(), m.width, "")
		if m.projectRegister.editingSetupToken {
			return m.language.text("输入单独授权的私有 OAuth JSON 路径"), input,
				m.language.text("Enter 返回 · F2 清空 · Esc 取消"), m.language.text("本机绝对路径 · 留空使用账号 MCP 凭据")
		}
		hint := ""
		if m.projectRegister.managedDirectory != "" {
			hint = m.language.text("留空按 Enter 预览专用目录")
		}
		return m.language.text("输入目标机已有目录的绝对路径"), input,
			m.language.text("Enter 预览 F3 授权 F2 清空 Esc 取消"), hint
	case tuiProjectRegisterConfirm:
		title := m.language.text("确认登记 T3 项目")
		actions := m.language.text("将在 T3 查找或登记项目。配置需在差异页按 s 保存。")
		if m.input.Value() == "" {
			title = m.language.text("确认创建本机目录并登记 T3")
			actions = m.language.text("配置需在差异页按 s 保存。")
		}
		return title, m.review.View(), m.language.text("y 确认 · Esc 取消 · ↑↓←→ 滚动"), actions
	default:
		return m.language.text("正在登记 T3 项目…"), m.language.text("请等待 T3 返回结果。"), "", ""
	}
}
