package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIProfileBundleImport struct {
	source, name, patch string
	plan                profileBundleImportPlan
}

type configTUIProfileBundlePreview struct {
	plan profileBundleImportPlan
	err  error
}

type configTUIProfileBundleApplied struct{ err error }

func (m *configTUI) editProfileBundleImport(status string) tea.Cmd {
	bundle := &m.profiles.bundle
	bundle.plan = profileBundleImportPlan{}
	m.screen, m.status = tuiProfileBundlePath, status
	return m.openInput(bundle.source)
}

func (m *configTUI) profileBundleImportKey(key string) (tea.Cmd, bool) {
	bundle := &m.profiles.bundle
	if key == "esc" {
		m.input.Blur()
		m.input.Reset()
		*bundle = configTUIProfileBundleImport{}
		m.screen, m.status = tuiProfiles, ""
		return nil, true
	}
	if m.screen == tuiProfileBundleReview {
		switch key {
		case "e":
			return m.editProfileBundleImport(""), true
		case "y":
			return m.applyProfileBundleImport(), true
		default:
			return nil, false
		}
	}
	switch key {
	case "f2":
		m.input.Reset()
	case "ctrl+e":
		if m.screen != tuiProfileBundleName {
			return nil, false
		}
		bundle.name = strings.TrimSpace(m.input.Value())
		m.screen, m.status = tuiProfileBundlePatch, ""
		return m.openInput(bundle.patch), true
	case "tab", "shift+tab", "enter":
		value := strings.TrimSpace(m.input.Value())
		switch m.screen {
		case tuiProfileBundlePath:
			bundle.source = value
		case tuiProfileBundlePatch:
			bundle.patch = value
		case tuiProfileBundleName:
			bundle.name = value
			if key == "enter" {
				return m.previewProfileBundleImport(), true
			}
			return m.editProfileBundleImport(""), true
		}
		m.screen, m.status = tuiProfileBundleName, ""
		return m.openInput(bundle.name), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) previewProfileBundleImport() tea.Cmd {
	registry, bundle := m.profiles.registry, m.profiles.bundle
	if bundle.source == "" {
		return m.editProfileBundleImport("请输入私有导出包路径")
	}
	m.input.Blur()
	m.screen, m.status = tuiProfileBundleReading, ""
	return func() tea.Msg {
		plan, err := registry.previewBundleImport(bundle.name, bundle.source, bundle.patch)
		return configTUIProfileBundlePreview{plan: plan, err: err}
	}
}

func (m *configTUI) receiveProfileBundlePreview(msg configTUIProfileBundlePreview) tea.Cmd {
	if m.screen != tuiProfileBundleReading {
		return nil
	}
	if msg.err != nil {
		return m.editProfileBundleImport("预览失败 · " + msg.err.Error())
	}
	m.profiles.bundle.plan = msg.plan
	m.screen, m.status = tuiProfileBundleReview, ""
	m.resizeProfileBundleReview()
	m.review.SetXOffset(0)
	m.review.GotoTop()
	return nil
}

func (m *configTUI) applyProfileBundleImport() tea.Cmd {
	registry, plan := m.profiles.registry, m.profiles.bundle.plan
	m.screen, m.status = tuiProfileBundleApplying, ""
	return func() tea.Msg {
		_, err := registry.importReviewedBundle(plan.Name, plan.Source, plan.Patch, plan.Revision)
		return configTUIProfileBundleApplied{err: err}
	}
}

func (m *configTUI) receiveProfileBundleApplied(msg configTUIProfileBundleApplied) tea.Cmd {
	if m.screen != tuiProfileBundleApplying {
		return nil
	}
	if msg.err != nil {
		return m.editProfileBundleImport("导入失败 · " + msg.err.Error())
	}
	name := m.profiles.bundle.plan.Name
	m.profiles.bundle = configTUIProfileBundleImport{}
	m.input.Reset()
	m.showProfiles(false, name, name+" 已导入")
	return nil
}

func (m *configTUI) resizeProfileBundleReview() {
	plan := m.profiles.bundle.plan
	details := fmt.Sprintf("来源 %q\n包内配置 %s\n新配置 %s\n目标 %q\nApp 数量 · %d",
		plan.Source, plan.Profile, plan.Name, plan.Destination, len(plan.Accounts))
	tokenReference := "T3 令牌文件继续使用原引用。"
	if plan.Patch != "" {
		fields := strings.Join(plan.PatchedFields, "\n  ")
		if fields == "" {
			fields = "无"
		}
		details += fmt.Sprintf("\n调整文件 %q\n调整字段\n  %s", plan.Patch, fields)
		tokenReference = "T3 令牌文件使用调整后配置中的引用。"
	}
	details += "\n为各 App 写入新的私有 App Secret 文件，分配独立状态目录。\n接收授权关闭。" + tokenReference
	m.review.Width, m.review.Height = m.width, m.bodyHeight()
	m.review.SetContent(ansi.Hardwrap(configTUIPlain(details), m.width, true))
	m.review.SetYOffset(m.review.YOffset)
}

func (m *configTUI) profileBundleImportView() (title, body, shortcuts, actions string) {
	switch m.screen {
	case tuiProfileBundlePath:
		return "导入私有包 · 来源 JSON 路径", ansi.Truncate(m.input.View(), m.width, ""),
			"Enter / Tab 下一项 · F2 清空 · Esc 取消", ""
	case tuiProfileBundleName:
		return "导入私有包 · 新配置名称", m.input.View(), "Enter 预览 · Ctrl+E 编辑调整路径 · Tab 改来源 · F2 清空 · Esc 取消", ""
	case tuiProfileBundlePatch:
		return "导入私有包 · 调整 JSON 路径（可选）", ansi.Truncate(m.input.View(), m.width, ""),
			"Enter / Tab 返回名称 · F2 清空 · Esc 取消", ""
	case tuiProfileBundleReading:
		return "预览私有包", "正在读取导出包…", "", ""
	case tuiProfileBundleReview:
		return "确认导入私有包", m.review.View(), "y 确认导入 · e 修改 · Esc 取消 · ↑↓ 滚动", ""
	default:
		return "导入私有包", "正在写入新配置和 App Secret 文件…", "", ""
	}
}
