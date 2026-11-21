package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var configTUILegacyImportLabels = [...]string{
	"旧 JSON 文件绝对路径",
	"旧 profile（单账号配置留空，V2 默认 activeProfile）",
	"新状态目录绝对路径（独立、未创建）",
	"App Secret 私有文件绝对路径",
}

type configTUILegacyImportForm struct {
	values       [len(configTUILegacyImportLabels)]string
	row          int
	reuseT3      bool
	base         string
	candidate    string
	plan         legacyCredentialPlan
	confirmation viewport.Model
	cancel       context.CancelFunc
}

type configTUILegacyCopyResult struct{ err error }

func (m *configTUI) openLegacyImport() tea.Cmd {
	m.legacyImport = configTUILegacyImportForm{base: m.draft}
	if path, err := detectLegacyConfig(); err == nil {
		m.legacyImport.values[0] = path
	}
	return m.showLegacyImport()
}

func (m *configTUI) showLegacyImport() tea.Cmd {
	f := &m.legacyImport
	m.screen, m.status = tuiImportLegacy, ""
	return m.openInput(f.values[f.row])
}

func (m *configTUI) legacyImportKey(key string) (tea.Cmd, bool) {
	f := &m.legacyImport
	switch key {
	case "esc":
		m.input.Blur()
		m.legacyImport = configTUILegacyImportForm{}
		m.screen, m.status = tuiBrowse, ""
	case "f2":
		m.input.Reset()
	case "ctrl+t":
		f.reuseT3 = !f.reuseT3
	case "down", "tab", "enter", "up", "shift+tab":
		f.values[f.row] = m.input.Value()
		delta := 1
		if key == "up" || key == "shift+tab" {
			delta = -1
		}
		n := len(f.values)
		f.row = (f.row + delta + n) % n
		return m.openInput(f.values[f.row]), true
	case "ctrl+s":
		f.values[f.row] = m.input.Value()
		request := legacyConfigImport{
			SourcePath: f.values[0], Profile: f.values[1], StateDir: f.values[2], AppSecretFile: f.values[3],
			ReuseT3: f.reuseT3,
		}
		f.plan, f.candidate = legacyCredentialPlan{}, ""
		candidate, plan, err := request.preview([]byte(f.base))
		if candidate == nil {
			m.status = err.Error()
			return nil, true
		}
		m.input.Blur()
		m.setDraft(candidate)
		m.preview()
		if err != nil {
			m.status = err.Error()
		} else if plan.Destination != "" && (plan.Inline || m.legacyProvider != "") && m.reviewed != nil {
			f.plan, f.candidate = plan, string(candidate)
		}
	default:
		return nil, false
	}
	return nil, true
}

func (f *configTUILegacyImportForm) matches(draft string, reviewed []byte) bool {
	return f.candidate != "" && f.candidate == draft && f.candidate == string(reviewed)
}

const legacyTUICandidateChanged = "候选配置已变化，请重新导入并预览凭据复制"

func (m *configTUI) openLegacyCopy() {
	f := &m.legacyImport
	if f.candidate == "" {
		return
	}
	if !f.matches(m.draft, m.reviewed) {
		m.status = legacyTUICandidateChanged
		return
	}
	m.screen, m.status = tuiLegacyCopyConfirm, ""
	f.confirmation = viewport.New(m.width, m.bodyHeight())
	m.resizeLegacyCopyReview()
}

func (m *configTUI) resizeLegacyCopyReview() {
	f := &m.legacyImport
	f.confirmation.Width, f.confirmation.Height = m.width, m.bodyHeight()
	source := "单账号配置"
	if !f.plan.Inline {
		source = fmt.Sprintf("Profile %q", f.plan.Profile)
	}
	details := fmt.Sprintf("%s\nApp %s\n私有文件 %s", source, f.plan.AppID, strings.ReplaceAll(f.plan.Destination, "\n", "�"))
	f.confirmation.SetContent(ansi.Hardwrap(configTUIPlain(details), m.width, true))
}

func (m *configTUI) legacyCopyKey(key string) (tea.Cmd, bool) {
	f := &m.legacyImport
	switch key {
	case "esc":
		m.screen, m.status = tuiReview, "已取消复制 App Secret"
		return nil, true
	case "y":
		if !f.matches(m.draft, m.reviewed) {
			m.screen, m.status = tuiReview, legacyTUICandidateChanged
			return nil, true
		}
		ctx, cancel := context.WithCancel(context.Background())
		f.cancel = cancel
		plan, provider, candidate, copySecret := f.plan, m.legacyProvider, []byte(f.candidate), m.copyLegacy
		m.screen, m.status = tuiLegacyCopyPending, ""
		return func() tea.Msg {
			defer cancel()
			if ctx.Err() != nil {
				return configTUILegacyCopyResult{err: errors.New("已取消复制 App Secret")}
			}
			return configTUILegacyCopyResult{err: copySecret(plan, ctx, provider, candidate)}
		}, true
	}
	return nil, false
}

func (m *configTUI) receiveLegacyCopy(result configTUILegacyCopyResult) {
	if m.screen != tuiLegacyCopyPending {
		return
	}
	m.screen, m.legacyImport.cancel = tuiReview, nil
	if result.err != nil {
		m.status = result.err.Error()
		return
	}
	m.legacyImport = configTUILegacyImportForm{}
	m.status = "App Secret 已复制到私有文件"
}

func (m *configTUI) legacyImportView() (title, body, shortcuts, actions string) {
	title = "导入 lark-channel-bridge · " + configTUILegacyImportLabels[m.legacyImport.row]
	body = ansi.Truncate(m.input.View(), m.width, "")
	if m.bodyHeight() > 1 {
		body += "\n" + ansi.Truncate("单路由模板可沿用旧用户名单。启用前请核对 T3 目标。", m.width, "…")
	}
	if m.bodyHeight() > 2 {
		body += "\n" + ansi.Truncate("可导入显示设置、并发额度和允许群的 @ 规则。", m.width, "…")
	}
	reuse := "关闭"
	if m.legacyImport.reuseT3 {
		reuse = "开启"
	}
	shortcuts = "Ctrl+S 预览 · F2 清空 · Esc 取消 · Tab 下一项 · Ctrl+T 沿用 T3 " + reuse
	actions = "旧单账号缺用户名单时沿用底稿用户。关闭所有账号的接收授权。"
	return
}
