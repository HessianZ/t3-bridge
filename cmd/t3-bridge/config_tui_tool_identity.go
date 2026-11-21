package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type configTUIToolIdentity struct {
	check     func(context.Context, webSource, string, string) (workspaceToolIdentity, error)
	request   int
	cancel    context.CancelFunc
	selection workspaceToolIdentity
}

type configTUIToolIdentityResult struct {
	request int
	result  workspaceToolIdentity
	err     error
}

func (m *configTUI) openToolIdentity() tea.Cmd {
	source := m.source
	switch {
	case m.route == "":
		m.status = "先打开要核验工具身份的路由"
		return nil
	case source.Path == "" || source.Revision == "" || source.Revision == "missing" || m.draft != string(source.JSON):
		m.status = "请先保存当前配置，再核验工具身份"
		return nil
	}
	var appID string
	if err := json.Unmarshal(m.accounts[m.account]["appId"], &appID); err != nil || appID == "" {
		m.status = "请核对当前账号的 App ID"
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.toolIdentity.request++
	m.toolIdentity.cancel = cancel
	routeID := m.route
	m.toolIdentity.selection = workspaceToolIdentity{AppID: appID, RouteID: routeID}
	m.screen, m.status = tuiToolIdentityLoading, ""
	request, check := m.toolIdentity.request, m.toolIdentity.check
	return func() tea.Msg {
		defer cancel()
		result, err := check(ctx, source, appID, routeID)
		return configTUIToolIdentityResult{request: request, result: result, err: err}
	}
}

func (m *configTUI) toolIdentityKey(key string) bool {
	if key != "esc" {
		return false
	}
	if m.toolIdentity.cancel != nil {
		m.toolIdentity.cancel()
		m.toolIdentity.cancel = nil
	}
	m.screen, m.status = tuiBrowse, ""
	return true
}

func (m *configTUI) receiveToolIdentity(msg configTUIToolIdentityResult) {
	if m.screen != tuiToolIdentityLoading || msg.request != m.toolIdentity.request {
		return
	}
	m.toolIdentity.cancel = nil
	m.toolIdentity.selection.Identity = msg.result.Identity
	m.screen = tuiToolIdentityResult
	outcome := "飞书工具身份核验通过"
	switch {
	case errors.Is(msg.err, errToolIdentityConfigChanged):
		outcome = errToolIdentityConfigChanged.Error()
	case msg.err != nil:
		outcome = "核验未通过 · 请核对本机 lark-cli、路由身份绑定及飞书授权"
		var failure toolIdentityProbeFailure
		if errors.As(msg.err, &failure) {
			outcome = "核验未通过 · " + failure.Error()
		}
	}
	m.review.Height = m.bodyHeight()
	m.review.SetContent(toolIdentitySelectionText(m.toolIdentity.selection) + "\n\n" + configTUIPlain(outcome))
	m.review.SetXOffset(0)
	m.review.GotoTop()
}

func toolIdentitySelectionText(identity workspaceToolIdentity) string {
	oneLine := func(value string) string {
		return configTUIPlain(strings.ReplaceAll(value, "\n", " "))
	}
	text := "App ID · " + oneLine(identity.AppID) + "\n路由 ID · " + oneLine(identity.RouteID)
	if identity.Identity != "" {
		text += "\n身份模式 · " + oneLine(identity.Identity)
	}
	return text
}

func (m *configTUI) toolIdentityView() (title, body, shortcuts, actions string) {
	actions = "使用已保存的配置执行飞书只读检查"
	if m.screen == tuiToolIdentityLoading {
		return "正在核验飞书工具身份…", toolIdentitySelectionText(m.toolIdentity.selection), "Esc 取消", actions
	}
	return "飞书工具身份核验结果", m.review.View(), "↑↓←→ 滚动 · Esc 返回路由", actions
}
