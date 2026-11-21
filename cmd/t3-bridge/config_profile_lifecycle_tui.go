package main

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIProfileLifecycle struct {
	request    int
	name, dest string
	purge      profilePurgePlan
	export     profileExportPlan
}

type configTUIProfilePreview struct {
	request int
	purge   profilePurgePlan
	export  profileExportPlan
	err     error
}

type configTUIProfileApplied struct{ err error }

func (m *configTUI) previewProfileLifecycle(name, dest string) tea.Cmd {
	p := &m.profiles
	request, registry := p.lifecycle.request+1, p.registry
	p.lifecycle = configTUIProfileLifecycle{request: request, name: name, dest: dest}
	m.screen, m.status = tuiProfilePreviewLoading, ""
	return func() tea.Msg {
		result := configTUIProfilePreview{request: request}
		if dest == "" {
			result.purge, result.err = registry.previewPurge(name)
		} else {
			result.export, result.err = registry.previewExport(name)
		}
		return result
	}
}

func (m *configTUI) receiveProfileLifecyclePreview(msg configTUIProfilePreview) tea.Cmd {
	l := &m.profiles.lifecycle
	if m.screen != tuiProfilePreviewLoading || l.request != msg.request {
		return nil
	}
	if msg.err != nil {
		m.showProfiles(false, l.name, "预览失败 · "+msg.err.Error())
		return nil
	}
	l.purge, l.export = msg.purge, msg.export
	m.screen = tuiConfirmProfileExport
	if l.dest == "" {
		m.screen = tuiConfirmProfilePurge
	}
	m.resizeProfileLifecycleReview()
	m.review.SetXOffset(0)
	m.review.GotoTop()
	if m.screen == tuiConfirmProfilePurge {
		return m.openInput("")
	}
	return nil
}

func (m *configTUI) profileLifecycleKey(key string) (tea.Cmd, bool) {
	l := &m.profiles.lifecycle
	if m.screen == tuiProfileApplying {
		return nil, true
	}
	if key == "esc" {
		m.input.Blur()
		m.input.Reset()
		m.screen, m.status = tuiProfiles, ""
		return nil, true
	}
	switch m.screen {
	case tuiProfileExportPath:
		if key != "enter" {
			return nil, false
		}
		dest := strings.TrimSpace(m.input.Value())
		if !filepath.IsAbs(dest) || !strings.EqualFold(filepath.Ext(dest), ".json") {
			m.status = "请输入新 JSON 文件的绝对路径"
			return nil, true
		}
		m.input.Blur()
		return m.previewProfileLifecycle(l.name, filepath.Clean(dest)), true
	case tuiConfirmProfilePurge, tuiConfirmProfileExport:
		switch key {
		case "up":
			m.review.ScrollUp(1)
		case "down":
			m.review.ScrollDown(1)
		case "pgup":
			m.review.PageUp()
		case "pgdown":
			m.review.PageDown()
		default:
			if m.screen == tuiConfirmProfileExport {
				if key == "y" {
					return m.applyProfileLifecycle(), true
				}
				return nil, true
			}
			if key != "enter" {
				return nil, false
			}
			if m.input.Value() != l.purge.Name {
				m.status = "请输入完全一致的配置名称"
				return nil, true
			}
			return m.applyProfileLifecycle(), true
		}
	}
	return nil, true
}

func (m *configTUI) applyProfileLifecycle() tea.Cmd {
	registry, l := m.profiles.registry, m.profiles.lifecycle
	m.input.Blur()
	m.input.Reset()
	m.screen, m.status = tuiProfileApplying, ""
	return func() tea.Msg {
		if l.dest == "" {
			return configTUIProfileApplied{err: registry.applyPurge(l.name, l.purge.Revision)}
		}
		return configTUIProfileApplied{err: registry.exportFile(l.name, l.dest, l.export.Revision, true, false)}
	}
}

func (m *configTUI) receiveProfileLifecycleApplied(msg configTUIProfileApplied) {
	if m.screen != tuiProfileApplying {
		return
	}
	p := &m.profiles
	l := p.lifecycle
	if msg.err != nil {
		m.showProfiles(false, l.name, "操作失败 · "+msg.err.Error())
		return
	}
	if l.dest != "" {
		m.showProfiles(false, l.name, fmt.Sprintf("私有文件已导出 · %q", l.dest))
		return
	}
	if p.isOpen(m.source, l.name) {
		if l.purge.DefaultAfter != "" {
			m.openProfile(l.purge.DefaultAfter)
		}
		if p.isOpen(m.source, l.name) {
			m.loadSource(p.fallback)
		}
	}
	m.showProfiles(false, "", l.name+" 已永久清除")
}

func (m *configTUI) resizeProfileLifecycleReview() {
	l := &m.profiles.lifecycle
	m.review.Width, m.review.Height = m.width, m.bodyHeight()
	var details string
	if m.screen == tuiConfirmProfilePurge {
		m.review.Height = max(1, m.review.Height-1)
		plan := l.purge
		lines := []string{fmt.Sprintf("配置 %s · 目录 %q", plan.Name, plan.ProfileDir)}
		if m.discardsProfileDraft(plan.Name) {
			lines = append(lines, "确认清除将丢弃当前草稿。")
		}
		lines = append(lines, "清除路径")
		for _, path := range plan.Paths {
			lines = append(lines, fmt.Sprintf("  %q", path))
		}
		lines = append(lines, "保留项")
		if len(plan.Retained) == 0 {
			lines = append(lines, "  无")
		}
		for _, item := range plan.Retained {
			lines = append(lines, fmt.Sprintf("  %q", item))
		}
		defaultAfter := plan.DefaultAfter
		if defaultAfter == "" {
			defaultAfter = "无"
		}
		lines = append(lines, "完成后默认配置 · "+defaultAfter)
		details = strings.Join(lines, "\n")
	} else {
		details = fmt.Sprintf("配置 %s（已保存版本）\n来源 %q\n目的地 %q\n含 App Secret 的 App 数量 · %d",
			l.export.Name, l.export.Source, l.dest, l.export.Credentials)
	}
	m.review.SetContent(ansi.Hardwrap(configTUIPlain(details), m.width, true))
	m.review.SetYOffset(m.review.YOffset)
}

func (m *configTUI) profileLifecycleView() (title, body, shortcuts, actions string) {
	name := m.profiles.lifecycle.name
	switch m.screen {
	case tuiProfileExportPath:
		return "私有导出 · " + name, m.input.View(), "Enter 预览 · Esc 取消", "输入新 JSON 文件的绝对路径"
	case tuiProfilePreviewLoading:
		return "预览命名配置 · " + name, "正在读取已保存的配置…", "Esc 取消", ""
	case tuiConfirmProfilePurge:
		return "确认永久清除 · " + name, m.review.View() + "\n" + m.input.View(),
			"复输配置名后按 Enter · Esc 取消 · ↑↓ 滚动", ""
	case tuiConfirmProfileExport:
		return "确认私有导出 · " + name, m.review.View(), "y 确认导出 · Esc 取消 · ↑↓ 滚动", ""
	default:
		action := "永久清除"
		if m.profiles.lifecycle.dest != "" {
			action = "写入私有文件"
		}
		return action + " · " + name, "正在" + action + "…", "", ""
	}
}
