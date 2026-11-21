package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const catalogOperateAccessWarning = "本机 OAuth 无法派发，请重新授权（至少监督权限）"

type configTUICatalog struct {
	request       int
	cancel        context.CancelFunc
	result        bridge.T3Catalog
	models        []configTUICatalogModel
	project       int
	choosingModel bool
	row           int
	options       *configTUICatalogOptions
}

type configTUICatalogModel struct {
	provider bridge.T3Provider
	model    bridge.T3Model
}

type configTUICatalogOptions struct {
	row    int
	fields []configTUICatalogOption
	values map[string]any
}

type configTUICatalogOption struct {
	id      string
	choices []any // The first choice is nil and removes the option override.
}

type configTUICatalogResult struct {
	request int
	result  bridge.T3Catalog
	err     error
}

func (m *configTUI) queryCatalog() tea.Cmd {
	var connection bridge.MCPConfig
	if err := json.Unmarshal(m.accounts[m.account]["mcp"], &connection); err != nil {
		m.status = "请先填写当前账号的 mcp 连接"
		return nil
	}
	var target bridge.Target
	_ = json.Unmarshal(m.routes[m.route], &target)
	if connection.EnvironmentID == "REPLACE_ENVIRONMENT_ID" {
		connection.EnvironmentID = ""
	}
	environment := target.EnvironmentID
	if environment == "REPLACE_ENVIRONMENT_ID" {
		environment = ""
	}
	search := strings.TrimSpace(m.input.Value())
	m.input.Blur()
	m.screen, m.status = tuiCatalogLoading, ""
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	request := m.catalog.request + 1
	m.catalog = configTUICatalog{request: request, cancel: cancel}
	return func() tea.Msg {
		defer cancel()
		catalog, err := bridge.DiscoverT3(ctx, connection, environment, search)
		catalog.SuggestProject(target.ProjectID, target.ProjectPath)
		return configTUICatalogResult{request: request, result: catalog, err: err}
	}
}

func (m *configTUI) receiveCatalog(msg configTUICatalogResult) {
	if m.screen != tuiCatalogLoading || msg.request != m.catalog.request {
		return
	}
	result := msg.result
	if msg.err != nil {
		m.screen, m.status = tuiBrowse, msg.err.Error()
		return
	}
	m.catalog.result = result
	for i, project := range result.Projects {
		if project.ID == result.SuggestedProjectID {
			m.catalog.row = i
			break
		}
	}
	for _, provider := range result.Providers {
		for _, model := range provider.Models {
			m.catalog.models = append(m.catalog.models, configTUICatalogModel{provider, model})
		}
	}
	switch {
	case len(result.Projects) == 0 || len(result.Providers) == 0:
		m.screen, m.status = tuiBrowse, "T3 没有返回可选择的项目和模型"
	case len(m.catalog.models) == 0:
		m.screen, m.status = tuiBrowse, "没有匹配的模型，请用其他名称查询"
	default:
		m.screen, m.status = tuiCatalogPick, "T3 授权上限 "+result.RuntimeMode
	}
	var link string
	if result.LinkAccess != "" {
		link = " · 关联权限 " + result.LinkAccess
	}
	if result.LinkAccess == "read-only" {
		link += "（只读，无法派发任务）"
	}
	if m.screen == tuiBrowse && result.OperateAccess == "missing" {
		m.status = catalogOperateAccessWarning + link + " · " + m.status
	} else {
		m.status += link
	}
}

func (m *configTUI) catalogKey(key string) (tea.Cmd, bool) {
	if key == "esc" {
		if m.catalog.options != nil && m.screen == tuiCatalogPick {
			m.catalog.options = nil
			return nil, true
		}
		if m.screen == tuiCatalogPick && m.catalog.choosingModel {
			m.catalog.row, m.catalog.choosingModel = m.catalog.project, false
			return nil, true
		}
		if m.screen == tuiCatalogLoading {
			m.catalog.cancel()
		}
		m.input.Blur()
		m.screen, m.status = tuiBrowse, ""
		return nil, true
	}
	switch m.screen {
	case tuiCatalogQuery:
		if key == "enter" {
			return m.queryCatalog(), true
		}
		return nil, false
	case tuiCatalogLoading:
		return nil, false
	}
	count := len(m.catalog.result.Projects)
	row := &m.catalog.row
	if m.catalog.choosingModel {
		count = len(m.catalog.models)
	}
	options := m.catalog.options
	if options != nil {
		count, row = len(options.fields), &options.row
	}
	switch key {
	case "up", "k":
		*row = max(0, *row-1)
	case "down", "j":
		*row = min(count-1, *row+1)
	case "home":
		*row = 0
	case "end":
		*row = count - 1
	case "left", "right":
		if options == nil {
			return nil, false
		}
		options.changeChoice(key)
	case "enter":
		switch {
		case options != nil:
			m.applyCatalogTarget()
		case m.catalog.choosingModel && len(m.catalog.models[m.catalog.row].model.Options) > 0:
			m.editCatalogOptions()
		case m.catalog.choosingModel:
			m.applyCatalogTarget()
		default:
			m.catalog.project, m.catalog.row, m.catalog.choosingModel = m.catalog.row, 0, true
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *configTUI) editCatalogOptions() {
	values, sameTarget := m.catalogCurrentOptions()
	if !sameTarget || values == nil {
		values = make(map[string]any)
	}
	options := &configTUICatalogOptions{values: values}
	for _, option := range m.catalog.models[m.catalog.row].model.Options {
		field := configTUICatalogOption{id: option.ID, choices: []any{nil}}
		for _, value := range option.Values {
			switch value.(type) {
			case bool, string:
				field.choices = append(field.choices, value)
			}
		}
		options.fields = append(options.fields, field)
	}
	m.catalog.options = options
}

func (field configTUICatalogOption) choiceIndex(values map[string]any) int {
	value, exists := values[field.id]
	if !exists {
		return 0
	}
	if index := slices.Index(field.choices[1:], value); index >= 0 {
		return index + 1
	}
	return len(field.choices)
}

func (options *configTUICatalogOptions) changeChoice(key string) {
	field := options.fields[options.row]
	index := field.choiceIndex(options.values)
	if key == "left" {
		index--
		if index < 0 {
			index = len(field.choices) - 1
		}
	} else {
		index++
		if index >= len(field.choices) {
			index = 0
		}
	}
	if choice := field.choices[index]; choice != nil {
		options.values[field.id] = choice
	} else {
		delete(options.values, field.id)
	}
}

func (m *configTUI) catalogView() (title, body, shortcuts, actions string) {
	switch m.screen {
	case tuiCatalogQuery:
		return m.language.text("查询 T3 项目和模型"), m.input.View(), m.language.text("Enter 查询 · Esc 返回"), m.language.text("可按模型名称搜索。留空列出常用模型。")
	case tuiCatalogLoading:
		return m.language.text("查询 T3 项目和模型"), m.language.text("正在读取 T3…"), m.language.text("Esc 取消"), ""
	}
	var labels []string
	row, height := m.catalog.row, m.bodyHeight()
	choices := m.language.text("↑↓ 选择")
	switch options := m.catalog.options; {
	case options != nil:
		title = m.language.text("编辑模型选项")
		row = options.row
		for _, field := range options.fields {
			value, exists := options.values[field.id]
			label := m.language.text("默认")
			if exists {
				encoded, _ := json.Marshal(value)
				label = string(encoded)
				if field.choiceIndex(options.values) == len(field.choices) {
					label = m.language.text("保留现值（目录未提供） ") + label
				}
			}
			labels = append(labels, field.id+" = "+label)
		}
		choices = m.language.text("↑↓ 选择选项 · ←→ 切换值")
		actions = m.language.text("Enter 应用到草稿 · Esc 返回模型")
	case m.catalog.choosingModel:
		title = m.language.text("选择模型")
		for _, item := range m.catalog.models {
			label := cmp.Or(item.model.Label, item.model.ID)
			provider := cmp.Or(item.provider.Name, item.provider.ID)
			labels = append(labels, label+" · "+provider+" · "+item.provider.Driver)
		}
		actions = m.language.text("Enter 应用到草稿 · Esc 返回项目")
		if len(m.catalog.models[row].model.Options) > 0 {
			actions = m.language.text("Enter 编辑选项 · Esc 返回项目")
		}
	default:
		title = m.language.text("选择项目")
		for _, project := range m.catalog.result.Projects {
			labels = append(labels, cmp.Or(project.Title, project.ID)+" · "+project.Root)
		}
		actions = m.language.text("Enter 选择模型 · Esc 返回路由")
	}
	var access string
	switch m.catalog.result.OperateAccess {
	case "granted":
		access = m.language.text("本机 OAuth 已授予派发权限")
	case "missing":
		access = catalogOperateAccessWarning
	case "unknown":
		access = m.language.text("本机 OAuth 派发权限未知（旧凭据未记录权限）")
	}
	if access != "" {
		height = max(1, height-1)
	}
	first := max(0, row-height+1)
	var lines []string
	for i := first; i < min(len(labels), first+height); i++ {
		marker := "  "
		if i == row {
			marker = "› "
		}
		label := strings.ReplaceAll(labels[i], "\n", " ")
		lines = append(lines, ansi.Truncate(configTUIPlain(marker+label), m.width, "…"))
	}
	if access != "" {
		lines = append(lines, ansi.Truncate(access, m.width, "…"))
	}
	return title, strings.Join(lines, "\n"), fmt.Sprintf("%s · %d/%d", choices, row+1, len(labels)), actions
}

func (m *configTUI) catalogCurrentOptions() (map[string]any, bool) {
	var current bridge.Target
	decoder := json.NewDecoder(bytes.NewReader(m.routes[m.route]))
	decoder.UseNumber()
	_ = decoder.Decode(&current)
	selected := m.catalog.models[m.catalog.row]
	return current.Options, current.EnvironmentID == m.catalog.result.EnvironmentID && current.Driver == selected.provider.Driver &&
		current.ProviderID == selected.provider.ID && current.Model == selected.model.ID
}

func (m *configTUI) applyCatalogTarget() {
	result := m.catalog.result
	project := result.Projects[m.catalog.project]
	selected := m.catalog.models[m.catalog.row]
	fields := []configTUIEdit{
		{Path: m.accountPath("mcp", "environmentId"), Value: result.LocalEnvironmentID},
		{Path: m.accountPath("routes", m.route, "environmentId"), Value: result.EnvironmentID},
		{Path: m.accountPath("routes", m.route, "projectId"), Value: project.ID},
		{Path: m.accountPath("routes", m.route, "projectName"), Value: project.Title},
		{Path: m.accountPath("routes", m.route, "projectPath"), Value: project.Root},
		{Path: m.accountPath("routes", m.route, "driver"), Value: selected.provider.Driver},
		{Path: m.accountPath("routes", m.route, "providerId"), Value: selected.provider.ID},
		{Path: m.accountPath("routes", m.route, "model"), Value: selected.model.ID},
	}
	if options := m.catalog.options; options != nil {
		fields = append(fields, configTUIEdit{Path: m.accountPath("routes", m.route, "options"), Value: options.values})
	} else if _, sameTarget := m.catalogCurrentOptions(); !sameTarget {
		fields = append(fields, configTUIEdit{Path: m.accountPath("routes", m.route, "options"), Value: json.RawMessage("{}")})
	}
	if m.applyChanges(fields...) {
		m.catalog.options = nil
		m.screen = tuiBrowse
		switch {
		case result.OperateAccess == "missing":
			m.status = catalogOperateAccessWarning
			if result.LinkAccess == "read-only" {
				m.status += " · 关联环境只读"
			}
			m.status += " · 已填入草稿"
		case result.LinkAccess == "read-only":
			m.status = "已填入 T3 目标 · 关联环境为只读，无法派发任务"
		default:
			m.status = "已填入 T3 目标，请核对模型选项和执行权限"
		}
	}
}
