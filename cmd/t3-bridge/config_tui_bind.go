package main

import (
	"context"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
)

type configTUIBinding struct {
	client        *qrRegistration
	stop          context.CancelFunc
	id            string
	view          qrBindingView
	code          string
	comments      bool
	selectedAppID string
	replaceAppID  string
	manual        configTUIManualBinding
}

type configTUIBindingResult struct {
	id   string
	view qrBindingView
}

func (m *configTUI) openFeishuBinding() {
	c, err := bridge.ParseConfig([]byte(m.draft))
	if err != nil {
		m.status = err.Error()
		return
	}
	m.binding = configTUIBinding{selectedAppID: c.Accounts()[m.account].AppID}
	m.screen, m.status = tuiBindReview, ""
}

func (m *configTUI) closeFeishuBinding() {
	if m.binding.stop != nil {
		m.binding.stop()
	}
	m.binding = configTUIBinding{}
}

func (m *configTUI) feishuBindingKey(key string) (tea.Cmd, bool) {
	switch key {
	case "esc":
		m.closeFeishuBinding()
		m.screen, m.status = tuiBrowse, ""
	case "r":
		if m.screen == tuiBindReview {
			m.binding.comments = !m.binding.comments
		}
	case "m":
		if m.screen == tuiBindReview {
			return m.openManualBinding(), true
		}
	case "x":
		if m.screen != tuiBindReview {
			return nil, true
		}
		m.status = ""
		if m.binding.replaceAppID != "" {
			m.binding.replaceAppID = ""
			return nil, true
		}
		if err := validateReplacementBinding([]byte(m.draft), m.binding.selectedAppID); err != nil {
			m.status = err.Error()
			return nil, true
		}
		m.binding.replaceAppID = m.binding.selectedAppID
	case "enter":
		if m.screen == tuiBindReview {
			ctx, stop := context.WithCancel(context.Background())
			m.binding.client = &qrRegistration{ctx: ctx, register: registration.RegisterApp}
			m.binding.id, m.binding.stop = m.binding.client.startFor(m.binding.comments, m.binding.replaceAppID), stop
			m.screen = tuiBindProgress
			return m.pollFeishuBinding(), true
		}
		if m.binding.view.State == "scanned" {
			next, err := m.binding.client.apply([]byte(m.draft), m.binding.id)
			if err != nil {
				m.status = err.Error()
				return nil, true
			}
			replacement := m.binding.replaceAppID != ""
			m.closeFeishuBinding()
			m.setDraft(next)
			if replacement {
				m.preview()
				return nil, true
			}
			m.screen, m.status = tuiBrowse, "应用凭据已保存，草稿已更新"
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) pollFeishuBinding() tea.Cmd {
	client, id := m.binding.client, m.binding.id
	return tea.Tick(terminalBindingPoll, func(time.Time) tea.Msg {
		view, _ := client.status(id)
		return configTUIBindingResult{id, view}
	})
}

func (m *configTUI) receiveFeishuBinding(result configTUIBindingResult) tea.Cmd {
	if m.screen != tuiBindProgress || result.id != m.binding.id {
		return nil
	}
	switch result.view.State {
	case "starting", "pending", "scanned":
		firstScan := result.view.State == "scanned" && m.binding.view.State != "scanned"
		if result.view.URL != m.binding.view.URL {
			m.binding.code = ""
			if result.view.URL != "" {
				m.binding.code = terminalQRCode(result.view.URL)
			}
		}
		m.binding.view = result.view
		if firstScan {
			return tea.Batch(tea.ClearScreen, m.pollFeishuBinding())
		}
		return m.pollFeishuBinding()
	default:
		m.closeFeishuBinding()
		m.screen, m.status = tuiBrowse, "飞书扫码授权未完成"
		return nil
	}
}

func (m *configTUI) feishuBindingView() (title, body, shortcuts string) {
	title = m.language.text("绑定飞书或 Lark")
	oldAppID := m.binding.replaceAppID
	if oldAppID != "" {
		title = m.language.text("替换飞书或 Lark 应用")
	}
	if m.screen == tuiBindReview {
		body = m.language.text("云文档评论权限：关闭")
		if m.binding.comments {
			body = m.language.text("云文档评论权限：开启")
		}
		if oldAppID != "" {
			body = m.language.text("替换应用 ") + oldAppID + "\n" +
				m.language.text("仅带入所选账号的默认 T3 路由和连接。\n") +
				m.language.text("使用独立状态目录，仅授权扫码用户，禁用接收。\n") + body
			return title, body, m.language.text("Enter 准备扫码 · r 切换评论权限 · x 切回普通绑定 · Esc 返回")
		}
		return title, body, m.language.text("Enter 准备扫码 · r 切换评论权限 · x 替换所选应用 · Esc 返回")
	}
	view := m.binding.view
	if view.State == "scanned" {
		if oldAppID != "" {
			return title, oldAppID + " → " + view.AppID, m.language.text("Enter 保存私有凭据并审阅草稿 · Esc 取消")
		}
		return title, m.language.text("已授权应用 ") + view.AppID, m.language.text("Enter 保存私有凭据并更新草稿 · Esc 取消")
	}
	if view.URL == "" {
		return title, m.language.text("正在获取二维码…"), m.language.text("Esc 取消")
	}
	return title, m.terminalAuthorizationBody(view.URL, m.binding.code), m.language.text("Esc 取消")
}

func (m *configTUI) terminalAuthorizationBody(address, code string) string {
	link := ansi.SetHyperlink(address) + "打开飞书或 Lark 授权页" + ansi.ResetHyperlink()
	code = strings.TrimSuffix(code, "\n")
	width := 0
	for _, line := range strings.Split(code, "\n") {
		width = max(width, ansi.StringWidth(line))
	}
	if code != "" && m.width >= width && m.bodyHeight() >= strings.Count(code, "\n")+3 {
		return link + "\n" + code
	}
	return link + "\n" + ansi.Hardwrap(address, m.width, true)
}
