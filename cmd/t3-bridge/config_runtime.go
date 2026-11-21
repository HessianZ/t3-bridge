package main

import (
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_runtime.html
var configRuntimeHTML string

type runtimeServiceRow struct {
	Name, Status string
	PID          int
	Manageable   bool
}

type runtimeReceiverRow struct {
	ID, Version   string
	PID, AppCount int
	Accounts      []runtimeAccountRow
}

type runtimeAccountRow struct {
	Tenant, AppID    string
	ObservedAt       string
	ObservedDateTime string
	Diagnostics      *bridge.ReceiverDiagnostics
	History          []runtimeDiagnosticEvent
	Worker           *runtimeWorkerRow
	SettingsURL      string
	AccessURL        string
	ReconnectURL     string
	MeetingsURL      string
}

type runtimeDiagnosticEvent struct {
	ObservedAt, ObservedDateTime, Label string
}

type runtimeWorkerRow struct {
	RetryAt, RetryDateTime, Failure string
	Attempts                        int
	Exhausted                       bool
}

func configRuntimeHandler(profiles profileRegistry, listServices func() ([]serviceInventoryEntry, error)) http.HandlerFunc {
	page := template.Must(newWorkspacePage("runtime").Parse(configRuntimeHTML))
	var manageMu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}

		post := r.Method == http.MethodPost
		var name, action, label string
		response := http.NewResponseController(w)
		if post {
			name, action, label = readRuntimeServiceAction(w, r)
			if label == "" {
				http.Error(w, "无效的实例操作。", http.StatusBadRequest)
				return
			}
			// The service manager can take longer than the workspace response timeout.
			_ = response.SetWriteDeadline(time.Time{})
			manageMu.Lock()
		}

		view := struct {
			Services                                []runtimeServiceRow
			Receivers                               []runtimeReceiverRow
			ServiceUnavailable, ReceiverUnavailable bool
			ActionInstance, ActionResult            string
			UnregisterInstance                      string
			WorkspaceVersion                        string
		}{WorkspaceVersion: bridgeVersion()}
		entries, listErr := listServices()
		status := http.StatusOK
		if post {
			matchesInstance := func(entry serviceInventoryEntry) bool { return entry.name == name }
			index := slices.IndexFunc(entries, matchesInstance)
			switch {
			case listErr != nil:
				status, view.ActionResult = http.StatusServiceUnavailable, "无法读取受管实例清单，操作未执行。"
			case index < 0:
				status, view.ActionResult = http.StatusNotFound, "未找到该受管实例，操作未执行。"
			case entries[index].err != nil || entries[index].manage == nil:
				status, view.ActionResult = http.StatusConflict, "该实例当前无法管理，操作未执行。"
			case action == "review-unregister":
				view.UnregisterInstance = entries[index].name
			default:
				entry := entries[index]
				view.ActionInstance = entry.name
				manageErr := entry.manage(action)
				entries, listErr = listServices()
				switch {
				case manageErr != nil:
					status, view.ActionResult = http.StatusBadGateway, label+"请求失败或结果未确认。请查看下方状态。"
				case action != "unregister":
					view.ActionResult = label + "请求已提交。请查看下方状态。"
				case listErr != nil:
					status, view.ActionResult = http.StatusServiceUnavailable, "无法刷新受管实例清单，注销结果未确认。"
				case slices.ContainsFunc(entries, matchesInstance):
					status, view.ActionResult = http.StatusBadGateway, "刷新后仍在受管实例清单中，注销结果未确认。"
				default:
					view.ActionResult = "已注销。已从刷新后的受管实例清单移除。"
				}
			}
			manageMu.Unlock()
		}

		view.ServiceUnavailable = listErr != nil
		for _, entry := range entries {
			row := runtimeServiceRow{Name: entry.name, Status: entry.status, PID: entry.pid, Manageable: listErr == nil && entry.err == nil && entry.manage != nil}
			if entry.err != nil {
				row.Status, row.PID = "状态不可用", 0
			}
			view.Services = append(view.Services, row)
		}
		processes, err := readRuntimeReceivers(profiles)
		view.ReceiverUnavailable = err != nil
		registry := localReceiverProcesses(profiles)
		for _, process := range processes {
			row := runtimeReceiverRow{ID: process.ID, Version: process.Version, PID: process.PID, AppCount: len(process.Accounts)}
			for _, account := range process.Accounts {
				detail := runtimeAccountRow{Tenant: account.Tenant, AppID: account.AppID}
				if process.RunSettings {
					detail.SettingsURL = runtimeSettingsTarget{Process: process.ID, Account: account}.URL()
				}
				if process.AccessPolicy {
					detail.AccessURL = runtimeAccessTarget{Process: process.ID, Account: account}.URL()
				}
				if process.Reconnect {
					detail.ReconnectURL = runtimeReconnectTarget{Process: process.ID, Account: account}.URL()
				}
				if process.Meetings {
					detail.MeetingsURL = runtimeMeetingTarget{Process: process.ID, Account: account}.URL()
				}
				sample, worker := registry.receiverSnapshot(process, account)
				if sample != nil {
					detail.ObservedAt = sample.At.Local().Format("15:04:05")
					detail.ObservedDateTime = sample.At.Format(time.RFC3339)
					detail.Diagnostics = &sample.Data
					detail.History = runtimeDiagnosticHistory(sample.Events)
				} else if worker != nil {
					detail.Worker = &runtimeWorkerRow{
						Attempts:  worker.Attempts,
						Failure:   receiverFailureText(worker.Failure),
						Exhausted: worker.State == receiverWorkerExhausted,
					}
					if worker.State == receiverWorkerRetrying {
						detail.Worker.RetryAt = worker.RetryAt.Local().Format(time.DateTime)
						detail.Worker.RetryDateTime = worker.RetryAt.Format(time.RFC3339)
					}
				}
				row.Accounts = append(row.Accounts, detail)
			}
			view.Receivers = append(view.Receivers, row)
		}
		if post {
			_ = response.SetWriteDeadline(time.Now().Add(configWebWriteTimeout))
		}
		// Preserve the Origin header on same-origin form submissions.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func runtimeDiagnosticHistory(events []receiverDiagnosticEvent) []runtimeDiagnosticEvent {
	const limit = 6
	events = slices.Clone(events)
	slices.SortStableFunc(events, func(a, b receiverDiagnosticEvent) int { return b.At.Compare(a.At) })
	var history []runtimeDiagnosticEvent
	for _, event := range events {
		var label string
		switch event.Kind {
		case receiverDiagnosticMCPVerified:
			label = "T3 环境核验通过"
		case receiverDiagnosticMCPUnverified:
			label = "T3 环境未核验"
		case receiverDiagnosticFeishuReady:
			label = "飞书 WebSocket 已就绪"
		case receiverDiagnosticFeishuNotReady:
			label = "飞书 WebSocket 未就绪"
		case receiverDiagnosticReconcile:
			label = "待核对归零"
			if event.Count != 0 {
				label = fmt.Sprintf("待核对 %d", event.Count)
			}
		case receiverDiagnosticDeliveryPending:
			label = "待送达归零"
			if event.Count != 0 {
				label = fmt.Sprintf("待送达 %d", event.Count)
			}
		default:
			continue
		}
		history = append(history, runtimeDiagnosticEvent{
			ObservedAt:       event.At.Local().Format(time.DateTime),
			ObservedDateTime: event.At.Format(time.RFC3339),
			Label:            label,
		})
		if len(history) == limit {
			break
		}
	}
	return history
}

func runtimeRequestOriginAllowed(r *http.Request) bool {
	expected := "http://" + r.Host
	if r.Method == http.MethodPost {
		origins, sites := r.Header.Values("Origin"), r.Header.Values("Sec-Fetch-Site")
		return len(origins) == 1 && origins[0] == expected && (len(sites) == 0 || (len(sites) == 1 && sites[0] == "same-origin"))
	}
	// The workspace cross-origin middleware exempts GET and HEAD.
	site, origin := r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin")
	return (site == "" || site == "same-origin" || site == "none") && (origin == "" || origin == expected)
}

func readRuntimeServiceAction(w http.ResponseWriter, r *http.Request) (name, action, label string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if r.URL.RawQuery != "" || r.ParseForm() != nil ||
		len(r.PostForm["instance"]) != 1 || len(r.PostForm["action"]) != 1 {
		return "", "", ""
	}
	name, action = r.PostForm.Get("instance"), r.PostForm.Get("action")
	fields := 2
	if action == "unregister" {
		fields++
		confirm := r.PostForm["confirm"]
		if len(confirm) != 1 || confirm[0] != "unregister" {
			return "", "", ""
		}
	}
	if len(r.PostForm) != fields || !serviceInstanceName.MatchString(name) {
		return "", "", ""
	}
	switch action {
	case "start":
		label = "启动"
	case "stop":
		label = "停止"
	case "restart":
		label = "重启"
	case "review-unregister", "unregister":
		label = "注销"
	}
	return name, action, label
}

func readRuntimeReceivers(profiles profileRegistry) ([]receiverProcess, error) {
	if profiles.root == "" {
		return nil, errors.New("receiver registry unavailable")
	}
	registry := localReceiverProcesses(profiles)
	// Avoid creating the registry and its lock when the user has never run a receiver.
	if _, err := os.Lstat(registry.dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return registry.list()
}
