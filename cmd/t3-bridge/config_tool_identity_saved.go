package main

import (
	"cmp"
	"context"
	"errors"
	"path/filepath"
	"slices"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type workspaceToolIdentity struct {
	AppID, RouteID, Identity string
}

var errToolIdentityConfigChanged = errors.New("已保存的配置已变化或不可用，请重新打开配置后核验")

// Only the built-in probes produce these fixed diagnostics. CLI output is discarded.
type toolIdentityProbeFailure struct{ reason string }

func (e toolIdentityProbeFailure) Error() string { return e.reason }

func savedToolIdentityChoices(path string) (revision string, choices []workspaceToolIdentity, err error) {
	if !filepath.IsAbs(path) {
		return "", nil, errToolIdentityConfigChanged
	}
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil {
		return "", nil, err
	}
	cfg, err := bridge.ParseConfig(raw)
	if err != nil {
		return "", nil, err
	}
	for _, account := range cfg.Accounts() {
		for routeID, target := range account.Routes {
			if tool := target.ToolContext; tool != nil && (tool.BotBinding != nil || tool.UserBinding != nil) {
				choices = append(choices, workspaceToolIdentity{AppID: account.AppID, RouteID: routeID, Identity: tool.Identity})
			}
		}
	}
	slices.SortFunc(choices, func(a, b workspaceToolIdentity) int {
		return cmp.Or(cmp.Compare(a.AppID, b.AppID), cmp.Compare(a.RouteID, b.RouteID))
	})
	return configRevision(raw), choices, nil
}

func checkSavedToolIdentity(ctx context.Context, source webSource, appID, routeID string) (workspaceToolIdentity, error) {
	return verifySavedToolIdentity(ctx, source, appID, routeID, func(ctx context.Context, cfg bridge.Config, route string) error {
		if err := bridge.CheckToolIdentity(ctx, cfg, route); err != nil {
			return toolIdentityProbeFailure{reason: err.Error()}
		}
		return nil
	})
}

func verifySavedToolIdentity(ctx context.Context, source webSource, appID, routeID string,
	verify func(context.Context, bridge.Config, string) error,
) (workspaceToolIdentity, error) {
	if !filepath.IsAbs(source.Path) || source.Revision == "" || source.Revision == "missing" {
		return workspaceToolIdentity{}, errToolIdentityConfigChanged
	}
	raw, err := bridge.ReadPrivateConfigJSON(source.Path)
	if err != nil || configRevision(raw) != source.Revision {
		return workspaceToolIdentity{}, errToolIdentityConfigChanged
	}
	cfg, err := bridge.ParseConfig(raw)
	if err != nil {
		return workspaceToolIdentity{}, errors.New("请先校验并保存有效的配置")
	}
	for _, account := range cfg.Accounts() {
		if account.AppID != appID {
			continue
		}
		tool := account.Routes[routeID].ToolContext
		if tool == nil || (tool.BotBinding == nil && tool.UserBinding == nil) {
			return workspaceToolIdentity{}, errors.New("所选路由缺少本机飞书工具身份绑定")
		}
		result := workspaceToolIdentity{AppID: account.AppID, RouteID: routeID, Identity: tool.Identity}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		probeErr := verify(ctx, account, routeID)
		latest, err := bridge.ReadPrivateConfigJSON(source.Path)
		if err != nil || configRevision(latest) != source.Revision {
			return workspaceToolIdentity{}, errToolIdentityConfigChanged
		}
		return result, probeErr
	}
	return workspaceToolIdentity{}, errors.New("已保存的配置中没有所选应用")
}
