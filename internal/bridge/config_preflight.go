package bridge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type ReceiverPreflight struct {
	Passed      bool                       `json:"passed"`
	AllAccounts *ReceiverPreflightOutcome  `json:"allAccounts,omitempty"`
	Accounts    []ReceiverPreflightAccount `json:"accounts"`
}

type ReceiverPreflightOutcome struct {
	Passed bool `json:"passed"`
}

// MarkAccountFailed applies an account check to the relevant readiness summaries.
func (r *ReceiverPreflight) MarkAccountFailed(enabled bool) {
	if enabled {
		r.Passed = false
	}
	if r.AllAccounts != nil {
		r.AllAccounts.Passed = false
	}
}

type ReceiverPreflightAccount struct {
	Tenant  string                   `json:"tenant"`
	AppID   string                   `json:"appId"`
	Enabled bool                     `json:"enabled"`
	Checks  []ReceiverPreflightCheck `json:"checks,omitempty"`
}

type ReceiverPreflightCheck struct {
	Check      string              `json:"check"`
	Route      string              `json:"route,omitempty"`
	Status     string              `json:"status"`
	Detail     string              `json:"detail,omitempty"`
	Fields     []string            `json:"fields,omitempty"`
	LockHolder *ReceiverLockHolder `json:"lockHolder,omitempty"`
}

type ReceiverLockHolder struct {
	Profile string `json:"profile"`
	PID     int    `json:"pid"`
}

// ReceiverPlaceholderFields lists unresolved values in the shipped templates.
// Drafts can retain them until the corresponding account and routes are enabled.
func (c Config) ReceiverPlaceholderFields() []string {
	var fields []string
	check := func(field string, values ...string) {
		for _, v := range values {
			switch v {
			case "REPLACE_APP_ID", "REPLACE_OPEN_ID", "REPLACE_CHAT_ID",
				"REPLACE_ENVIRONMENT_ID", "REPLACE_PROJECT_ID", "REPLACE_PROVIDER_INSTANCE_ID", "REPLACE_MODEL_ID",
				"REPLACE_BUILD_ENVIRONMENT_ID", "REPLACE_BUILD_PROJECT_ID", "REPLACE_BUILD_PROVIDER_INSTANCE_ID", "REPLACE_BUILD_MODEL_ID",
				"REPLACE_REVIEW_PROVIDER_INSTANCE_ID", "REPLACE_REVIEW_MODEL_ID":
				fields = append(fields, field)
				return
			}
		}
	}
	check("appId", c.AppID)
	check("users", c.Users...)
	check("admins", c.Admins...)
	check("chats", c.Chats...)
	if c.DirectChats != nil {
		check("directChats", (*c.DirectChats)...)
	}
	check("eventChats", c.EventChats...)
	check("mcp.environmentId", c.MCP.EnvironmentID)
	for _, id := range slices.Sorted(maps.Keys(c.Routes)) {
		target := c.Routes[id]
		if target.UnavailableReason != "" {
			continue
		}
		prefix := fmt.Sprintf("routes[%q].", id)
		check(prefix+"environmentId", target.EnvironmentID)
		check(prefix+"projectId", target.ProjectID)
		check(prefix+"providerId", target.ProviderID)
		check(prefix+"model", target.Model)
		check(prefix+"users", target.Users...)
	}
	return fields
}

// CheckReceiverConfig uses the receiver's read-only identity, route and state lock checks.
// config has passed ParseConfig. The caller sets the overall deadline.
func CheckReceiverConfig(ctx context.Context, config Config, includeDisabled bool) ReceiverPreflight {
	return checkReceiverConfig(ctx, config, includeDisabled, CheckFeishuApp, func(ctx context.Context, config MCPConfig) (ToolCaller, func(), error) {
		client, err := ConnectVerifiedMCP(ctx, config)
		if err != nil {
			return nil, nil, err
		}
		return client, func() { client.Session.Close() }, nil
	})
}

func checkReceiverConfig(ctx context.Context, config Config, includeDisabled bool,
	checkFeishu func(context.Context, string, string, string) error,
	connectT3 func(context.Context, MCPConfig) (ToolCaller, func(), error),
) ReceiverPreflight {
	report := ReceiverPreflight{Passed: true}
	if includeDisabled {
		report.AllAccounts = &ReceiverPreflightOutcome{Passed: true}
	}
	enabled := 0
	accounts := config.Accounts()
	for _, account := range accounts {
		row := ReceiverPreflightAccount{
			Tenant: account.Tenant, AppID: account.AppID, Enabled: account.ReceiverAuthorized,
		}
		if row.Tenant == "" {
			row.Tenant = "feishu"
		}
		fields := account.ReceiverPlaceholderFields()
		if len(fields) != 0 {
			row.Checks = append(row.Checks, ReceiverPreflightCheck{
				Check: "setup", Status: "failed", Fields: fields, Detail: "请填写模板占位字段，再核验飞书和 T3",
			})
		}
		if row.Enabled {
			enabled++
		}
		if row.Enabled || includeDisabled {
			if len(fields) == 0 {
				feishuCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				row.Checks = append(row.Checks, runReceiverCheck(feishuCtx, "feishu", "", func() error {
					return checkFeishu(feishuCtx, account.AppID, account.AppSecretFile, row.Tenant)
				}))
				cancel()
				t3Ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				row.Checks = append(row.Checks, checkReceiverRoutes(t3Ctx, account, connectT3)...)
				cancel()
			}
			for _, check := range row.Checks {
				if check.Status != "ok" && check.Status != "disabled" {
					report.MarkAccountFailed(row.Enabled)
				}
			}
		}
		report.Accounts = append(report.Accounts, row)
	}
	for i, account := range accounts {
		check := checkReceiverStateLock(account.StateDir)
		row := &report.Accounts[i]
		row.Checks = append(row.Checks, check)
		if check.Status != "ok" {
			report.MarkAccountFailed(row.Enabled)
		}
	}
	report.Passed = report.Passed && enabled > 0
	return report
}

func checkReceiverStateLock(dir string) ReceiverPreflightCheck {
	check := ReceiverPreflightCheck{Check: "state-lock", Status: "unknown", Detail: "无法核对所选状态目录的锁"}
	lock, err := localfile.LockExisting(stateLockPath(dir), false)
	switch {
	case errors.Is(err, localfile.ErrLocked):
		check.Status, check.Detail = "failed", "所选状态目录正被接收端或其他状态操作占用"
	case err == nil:
		_ = lock.Close()
		check.Status, check.Detail = "ok", "所选状态目录的锁可用"
	case errors.Is(err, os.ErrNotExist):
		check.Status, check.Detail = "ok", "所选状态目录中没有锁文件"
	}
	return check
}

func checkReceiverRoutes(ctx context.Context, account Config,
	connect func(context.Context, MCPConfig) (ToolCaller, func(), error),
) []ReceiverPreflightCheck {
	var caller ToolCaller
	var closeSession func()
	environment := runReceiverCheck(ctx, "t3", "", func() error {
		var err error
		caller, closeSession, err = connect(ctx, account.MCP)
		return err
	})
	if closeSession != nil {
		defer closeSession()
	}
	checks := []ReceiverPreflightCheck{environment}
	t3 := T3{Calls: caller, Config: account.MCP}
	for _, id := range slices.Sorted(maps.Keys(account.Routes)) {
		target := account.Routes[id]
		if target.UnavailableReason != "" {
			checks = append(checks, ReceiverPreflightCheck{Check: "route", Route: id, Status: "disabled", Detail: "路由已标记为不可用"})
			continue
		}
		if environment.Status != "ok" {
			checks = append(checks, ReceiverPreflightCheck{
				Check: "route", Route: id, Status: "skipped", Detail: "T3 环境未通过核验",
			})
			continue
		}
		checks = append(checks, runReceiverCheck(ctx, "route", id, func() error {
			return t3.Preflight(ctx, target)
		}))
	}
	return checks
}

// The Feishu and T3 probes return fixed diagnostic messages. Their implementations
// discard remote response bodies and credential values before returning errors.
func runReceiverCheck(ctx context.Context, name, route string, run func() error) ReceiverPreflightCheck {
	check := ReceiverPreflightCheck{Check: name, Route: route, Status: "ok"}
	if ctx.Err() != nil {
		check.Status, check.Detail = "skipped", "预检已超时或取消"
		return check
	}
	if err := run(); err != nil {
		check.Status, check.Detail = "failed", err.Error()
		if ctx.Err() != nil {
			check.Detail = "预检已超时或取消"
		}
	}
	return check
}
