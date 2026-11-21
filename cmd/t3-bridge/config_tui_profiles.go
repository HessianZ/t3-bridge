package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type configTUIProfiles struct {
	registry    profileRegistry
	fallback    webSource
	active      string
	archived    bool
	entries     []string
	row         int
	create      [2]string
	createField int
	lifecycle   configTUIProfileLifecycle
	bundle      configTUIProfileBundleImport
}

func (p *configTUIProfiles) isOpen(source webSource, name string) bool {
	return source.Path == filepath.Join(p.registry.root, "profiles", name, "config.json")
}

func (m *configTUI) openProfiles() {
	p := &m.profiles
	if p.registry.root == "" {
		m.status = "无法定位命名配置目录"
		return
	}
	m.showProfiles(false, "", "")
	if i := slices.IndexFunc(p.entries, func(name string) bool { return p.isOpen(m.source, name) }); i >= 0 {
		p.row = i
	}
}

func (m *configTUI) showProfiles(archived bool, selected, status string) {
	p := &m.profiles
	p.archived, p.entries, p.active = archived, nil, ""
	m.screen, m.status = tuiProfiles, status
	var err error
	if archived {
		var dirs []os.DirEntry
		dirs, err = os.ReadDir(filepath.Join(p.registry.root, "profile-archive"))
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		for _, dir := range dirs {
			if _, ok := archivedProfileName(dir.Name()); ok && dir.IsDir() {
				p.entries = append(p.entries, dir.Name())
			}
		}
	} else {
		p.entries, err = p.registry.names()
		if err == nil {
			p.active, err = p.registry.active()
		}
	}
	if err != nil {
		m.status = strings.TrimSpace(status + " " + err.Error())
	}
	p.row = max(0, slices.Index(p.entries, selected))
}

func (m *configTUI) profileKey(key string) (tea.Cmd, bool) {
	p := &m.profiles
	if key == "esc" {
		m.status = ""
		if m.screen == tuiProfiles {
			m.screen = tuiBrowse
		} else {
			m.input.Blur()
			m.screen = tuiProfiles
		}
		return nil, true
	}
	switch m.screen {
	case tuiCreateProfile:
		switch key {
		case "tab", "shift+tab":
			p.create[p.createField] = m.input.Value()
			p.createField = 1 - p.createField
			return m.openInput(p.create[p.createField]), true
		case "enter":
			m.createProfile()
		default:
			return nil, false
		}
	case tuiConfirmProfileOpen:
		if key == "d" {
			m.openProfile(p.entries[p.row])
		}
	case tuiConfirmProfileArchive:
		name := p.entries[p.row]
		if m.discardsProfileDraft(name) {
			if key == "d" {
				m.archiveProfile(name)
			}
		} else if key == "enter" {
			m.archiveProfile(name)
		}
	case tuiProfiles:
		switch key {
		case "tab":
			m.showProfiles(!p.archived, "", "")
		case "up", "k":
			p.row = max(0, p.row-1)
		case "down", "j":
			p.row = min(max(0, len(p.entries)-1), p.row+1)
		case "n":
			if !p.archived {
				p.create, p.createField = [2]string{}, 0
				m.screen, m.status = tuiCreateProfile, ""
				return m.openInput(""), true
			}
		case "i":
			if !p.archived {
				p.bundle = configTUIProfileBundleImport{}
				return m.editProfileBundleImport(""), true
			}
		default:
			if len(p.entries) == 0 {
				return nil, true
			}
			name := p.entries[p.row]
			if p.archived {
				if key == "enter" {
					m.restoreProfile(name)
				}
				return nil, true
			}
			switch key {
			case "enter":
				if m.draft != string(m.source.JSON) {
					m.screen, m.status = tuiConfirmProfileOpen, ""
				} else {
					m.openProfile(name)
				}
			case "u":
				if m.runProfileAction("use", name) {
					m.showProfiles(false, name, name+" 已设为默认")
				}
			case "a":
				m.screen, m.status = tuiConfirmProfileArchive, ""
			case "x":
				return m.previewProfileLifecycle(name, ""), true
			case "e":
				p.lifecycle.name = name
				m.screen, m.status = tuiProfileExportPath, ""
				return m.openInput(""), true
			}
		}
	}
	return nil, true
}

func (m *configTUI) openProfile(name string) {
	registry := m.profiles.registry
	path, err := registry.existingFile(name)
	if err != nil {
		m.status = err.Error()
		return
	}
	source, err := configFileSource(path, nil)
	if err != nil {
		m.status = err.Error()
		return
	}
	if source.Revision == "missing" {
		m.status = "配置已移走，请重新打开列表"
		return
	}
	source.ID, source.Label, source.Profile = "profile-"+name, "命名配置 · "+name, true
	m.loadSource(source)
	m.screen = tuiBrowse
}

func (m *configTUI) createProfile() {
	p := &m.profiles
	p.create[p.createField] = m.input.Value()
	name := strings.TrimSpace(p.create[0])
	args := []string{"create", name}
	if from := p.create[1]; from != "" {
		args = append(args, "-from", from)
	}
	if m.runProfileAction(args...) {
		m.input.Blur()
		m.showProfiles(false, name, name+" 已新建")
	}
}

func (m *configTUI) discardsProfileDraft(name string) bool {
	return m.profiles.isOpen(m.source, name) && m.draft != string(m.source.JSON)
}

func (m *configTUI) archiveProfile(name string) {
	if !m.runProfileAction("archive", name) {
		return
	}
	if m.profiles.isOpen(m.source, name) {
		m.loadSource(m.profiles.fallback)
	}
	m.showProfiles(false, "", name+" 已归档。按 Tab 查看归档。")
}

func (m *configTUI) restoreProfile(id string) {
	path := filepath.Join(m.profiles.registry.root, "profile-archive", id, "config.json")
	if m.runProfileAction("restore", path) {
		name, _ := archivedProfileName(id)
		m.showProfiles(false, name, name+" 已恢复")
	}
}

func (m *configTUI) runProfileAction(args ...string) bool {
	if err := m.profiles.registry.run(args, io.Discard); err != nil {
		m.status = err.Error()
		return false
	}
	return true
}

func (m *configTUI) profileView() (title, body, shortcuts, actions string) {
	p := &m.profiles
	switch m.screen {
	case tuiCreateProfile:
		fields := [...]string{"配置名称", "来源 JSON 路径（可选）"}
		return "新建命名配置 · " + fields[p.createField], m.input.View(),
			"Tab 切换名称和来源 · Enter 新建 · Esc 取消", "来源留空时使用单路由模板"
	case tuiConfirmProfileOpen:
		return "打开命名配置 · " + p.entries[p.row], "当前草稿尚未保存。",
			"d 丢弃草稿并打开 · Esc 返回列表", "返回编辑后可用 p 预览并保存或导出"
	case tuiConfirmProfileArchive:
		name := p.entries[p.row]
		title = "归档命名配置 · " + name
		body = "将归档配置及备份，保留状态目录。"
		if p.isOpen(m.source, name) {
			body += "完成后回到启动模板。"
		}
		if m.discardsProfileDraft(name) {
			return title, body, "d 丢弃当前草稿并归档 · Esc 取消", "返回编辑后可用 p 预览并保存或导出"
		}
		return title, body, "Enter 归档 · Esc 取消", ""
	}
	title, actions, body = "命名配置", "n 新建", "暂无命名配置"
	shortcuts = "i 导入私有包 · ↑↓ 选择 · Tab 查看归档 · Esc 返回编辑"
	if p.archived {
		title, actions, body = "已归档的命名配置", "Enter 恢复", "暂无归档"
		shortcuts = "↑↓ 选择 · Tab 查看命名配置 · Esc 返回编辑"
	}
	if len(p.entries) != 0 {
		if !p.archived {
			actions = "Enter 打开 · u 设为默认 · n 新建 · a 归档 · x 清除 · e 私有导出"
		}
		height := m.bodyHeight()
		first := max(0, p.row-height+1)
		var lines []string
		for i := first; i < min(len(p.entries), first+height); i++ {
			name := p.entries[i]
			label := "  "
			if i == p.row {
				label = "› "
			}
			label += name
			if !p.archived {
				if name == p.active {
					label += " · 默认"
				}
				if p.isOpen(m.source, name) {
					label += " · 当前编辑"
				}
			}
			lines = append(lines, ansi.Truncate(configTUIPlain(label), m.width, "…"))
		}
		body = strings.Join(lines, "\n")
	}
	return title, body, shortcuts, actions
}
