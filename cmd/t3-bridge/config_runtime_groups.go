package main

import (
	"context"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_runtime_groups.html
var configRuntimeGroupsHTML string

//go:embed config_runtime_groups.js
var configRuntimeGroupsJS string

type runtimeGroupTarget struct {
	ProcessID, Tenant, AppID string
	PID                      int
	BatchGrant               bool
}

func (t runtimeGroupTarget) URL(chatID string) string {
	query := url.Values{"process": {t.ProcessID}, "tenant": {t.Tenant}, "appId": {t.AppID}}
	if chatID != "" {
		query.Set("chatId", chatID)
	}
	return "/runtime/groups?" + query.Encode()
}

type runtimeGroupBatchReview struct {
	mu      sync.Mutex
	target  runtimeGroupTarget
	chatIDs []string
	expires time.Time
}

func (r *runtimeGroupBatchReview) remember(target runtimeGroupTarget, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target, r.chatIDs, r.expires = target, ids, time.Now().Add(5*time.Minute)
}

func (r *runtimeGroupBatchReview) take(target runtimeGroupTarget, ids []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target != target || !slices.Equal(r.chatIDs, ids) || !time.Now().Before(r.expires) {
		return false
	}
	r.chatIDs = nil
	return true
}

func configRuntimeGroupsHandler(list func() ([]receiverProcess, error), operate func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error)) http.HandlerFunc {
	page := template.Must(newWorkspacePage("runtime groups").Funcs(template.FuncMap{
		"groupMessageEventText": groupMessageEventText,
	}).Parse(configRuntimeGroupsHTML))
	var review runtimeGroupBatchReview
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		post := r.Method == http.MethodPost
		var view struct {
			Targets       []runtimeGroupTarget
			Selected      *runtimeGroupTarget
			ChatID        string
			Access        *bridge.GroupAccessView
			BatchLimit    int
			BatchPreview  []bridge.GroupAccessEntry
			ScopeStatus   string
			ScopeGuidance string
			Notice        string
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "无效的群授权链接。", http.StatusBadRequest)
			return
		}
		var command bridge.GroupAccessRequest
		var batch string
		var batchIDs []string
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "无效的群授权操作。", http.StatusBadRequest)
				return
			}
			values = r.PostForm
			switch {
			case values.Has("batch"):
				batch, batchIDs = values.Get("batch"), values["groupId"]
				grant := bridge.GroupAccessRequest{ChatIDs: batchIDs, Access: "allow"}
				if len(values) != 5 || len(values["batch"]) != 1 || (batch != "preview" && batch != "apply") ||
					!validRuntimeReconnectTarget(values) || grant.ValidateBatchGrant() != nil {
					http.Error(w, "无效的批量群授权操作。", http.StatusBadRequest)
					return
				}
				if batch == "apply" {
					command = grant
				}
			case validGroupMutationForm(values):
				command = bridge.GroupAccessRequest{ChatID: values.Get("chatId"), Access: values.Get("access"), Mention: values.Get("mention")}
			default:
				http.Error(w, "无效的群授权操作。", http.StatusBadRequest)
				return
			}
		}
		processID, tenant, appID := values.Get("process"), values.Get("tenant"), values.Get("appId")
		if !post && values.Has("chatId") {
			view.ChatID = values.Get("chatId")
			if len(values["chatId"]) != 1 || view.ChatID == "" || len(values["appId"]) != 1 || appID == "" ||
				len(values["tenant"]) > 1 || (values.Has("process") && (len(values["process"]) != 1 || processID == "")) {
				http.Error(w, "无效的群授权链接。", http.StatusBadRequest)
				return
			}
			tenant = newReceiverGroupAccount(tenant, appID).Tenant
		}
		status := http.StatusOK
		processes, err := list()
		if err != nil {
			status, view.Notice = http.StatusServiceUnavailable, "接收进程清单暂不可用。"
		}
		var matches []runtimeGroupTarget
		for _, process := range processes {
			if !process.GroupAccess {
				continue
			}
			for _, account := range process.Accounts {
				target := runtimeGroupTarget{ProcessID: process.ID, Tenant: account.Tenant, AppID: account.AppID, PID: process.PID, BatchGrant: process.GroupBatchGrant}
				match := target.Tenant == tenant && target.AppID == appID &&
					(target.ProcessID == processID || (view.ChatID != "" && processID == ""))
				if match {
					matches = append(matches, target)
				}
				if view.ChatID == "" || match {
					view.Targets = append(view.Targets, target)
				}
			}
		}
		if status == http.StatusOK && (post || len(values) != 0) {
			switch len(matches) {
			case 0:
				status, view.Notice = http.StatusNotFound, "此接收进程或账号已不可用，请重新选择。"
			case 1:
				target := matches[0]
				view.Selected = &target
				if batch != "" && !target.BatchGrant {
					status, view.Notice = http.StatusConflict, "此接收进程不支持批量群授权。"
					break
				}
				if batch == "apply" && !review.take(target, batchIDs) {
					status, view.Notice = http.StatusConflict, "批量授权预览已失效，请重新预览后确认。"
					break
				}
				ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
				result, err := operate(ctx, target.ProcessID, target.Tenant, target.AppID, command)
				cancel()
				switch {
				case err != nil:
					status, view.Notice = http.StatusBadGateway, "在线群权限操作未确认，请重新读取状态再操作。"
				case batch == "preview":
					view.BatchPreview = previewRuntimeGroupBatch(result, batchIDs)
					if len(view.BatchPreview) == 0 {
						status, view.Notice = http.StatusConflict, "无法确认所选群均已入群且未授权，请重新读取权限后再选择。"
						break
					}
					review.remember(target, batchIDs)
				case post:
					chatID := ""
					if values.Get("returnToGroup") == "1" {
						chatID = command.ChatID
					}
					http.Redirect(w, r, target.URL(chatID)+"&updated=1", http.StatusSeeOther)
					return
				default:
					if view.ChatID != "" {
						i := slices.IndexFunc(result.Groups, func(group bridge.GroupAccessEntry) bool { return group.ID == view.ChatID })
						if i < 0 {
							status, view.Notice = http.StatusNotFound, "此接收账号的当前群状态中未找到该群。"
							break
						}
						result.Groups = result.Groups[i : i+1]
					}
					view.Access = &result
					if view.ChatID == "" && target.BatchGrant && result.JoinedAvailable && slices.ContainsFunc(result.Groups, groupBatchEligible) {
						view.BatchLimit = bridge.MaxGroupGrantBatch
					}
					view.ScopeStatus, view.ScopeGuidance = groupMessageScopeText(result.MessageScope)
					if values.Get("updated") == "1" {
						view.Notice = "群权限已更新。"
					}
				}
			default:
				view.Notice = "此账号有多个匹配的接收进程，请明确选择要管理的进程。"
				if batch != "" {
					status = http.StatusConflict
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func previewRuntimeGroupBatch(view bridge.GroupAccessView, ids []string) []bridge.GroupAccessEntry {
	if !view.JoinedAvailable {
		return nil
	}
	groups := make([]bridge.GroupAccessEntry, 0, len(ids))
	for _, id := range ids {
		i := slices.IndexFunc(view.Groups, func(group bridge.GroupAccessEntry) bool {
			return group.ID == id && groupBatchEligible(group)
		})
		if i < 0 {
			return nil
		}
		groups = append(groups, view.Groups[i])
	}
	return groups
}

func groupBatchEligible(group bridge.GroupAccessEntry) bool {
	return group.Joined && !group.Allowed
}

func groupMessageScopeText(scope bridge.GroupMessageScope) (status, guidance string) {
	switch scope {
	case bridge.GroupMessageScopeGranted:
		return "已授权", ""
	case bridge.GroupMessageScopeMissing:
		return "缺失", "请在管理员私聊发送 /config mention authorize 补授权。"
	default:
		return "未知", ""
	}
}

func groupMessageEventText(state string) string {
	switch state {
	case "subscribed":
		return "已订阅"
	case "missing":
		return "未订阅"
	case "unpublished":
		return "应用未发布"
	default:
		return "未知"
	}
}

func validGroupMutationForm(form url.Values) bool {
	for name, entries := range form {
		if len(entries) != 1 || !slices.Contains([]string{"process", "tenant", "appId", "chatId", "access", "mention", "returnToGroup"}, name) {
			return false
		}
	}
	access, mention := form.Get("access"), form.Get("mention")
	if !receiverProcessID.MatchString(form.Get("process")) || form.Get("appId") == "" || (form.Get("tenant") != "feishu" && form.Get("tenant") != "lark") ||
		(access == "" && mention == "") || (form.Get("chatId") == "" && access != "") ||
		(form.Has("returnToGroup") && (form.Get("returnToGroup") != "1" || form.Get("chatId") == "")) {
		return false
	}
	return slices.Contains([]string{"", "allow", "deny", "inherit"}, access) && slices.Contains([]string{"", "on", "off", "inherit"}, mention)
}
