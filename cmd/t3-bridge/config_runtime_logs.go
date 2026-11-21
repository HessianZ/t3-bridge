package main

import (
	_ "embed"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const serviceLogViewerLines = 100

// Allow a full instance name and App ID even when the query is percent-encoded.
const serviceLogViewerMaxQueryBytes = 768

func readLocalServiceLogs(instance string, telemetry bool) (string, error) {
	var output strings.Builder
	args := []string{"logs", "-instance", instance, "-lines", strconv.Itoa(serviceLogViewerLines)}
	if telemetry {
		args = append(args, "-telemetry")
	}
	if err := runHostService(args, &output); err != nil {
		return "", err
	}
	return output.String(), nil
}

//go:embed config_runtime_logs.html
var configRuntimeLogsHTML string

type serviceLogPageQuery struct {
	Instance  string
	Telemetry bool
	Account   string
	Selected  receiverProcessAccount
}

func parseServiceLogPageQuery(raw string) (serviceLogPageQuery, bool) {
	if len(raw) > serviceLogViewerMaxQueryBytes {
		return serviceLogPageQuery{}, false
	}
	query, err := url.ParseQuery(raw)
	name := query.Get("instance")
	// ParseQuery skips empty fields and groups duplicate keys.
	if err != nil || len(query) != strings.Count(raw, "&")+1 || !serviceInstanceName.MatchString(name) {
		return serviceLogPageQuery{}, false
	}
	selection := serviceLogPageQuery{Instance: name}
	if len(query) == 1 {
		return selection, true
	}
	if query.Get("format") != "telemetry" {
		return serviceLogPageQuery{}, false
	}
	selection.Telemetry = true
	if len(query) == 2 {
		return selection, true
	}
	if len(query) != 3 || !query.Has("account") {
		return serviceLogPageQuery{}, false
	}
	selection.Account = query.Get("account")
	if selection.Account == "all" {
		return selection, true
	}
	selection.Selected.Tenant, selection.Selected.AppID, _ = strings.Cut(selection.Account, ":")
	return selection, validServiceTelemetryAccount(selection.Selected)
}

func configRuntimeLogsHandler(readLogs func(string, bool) (string, error)) http.HandlerFunc {
	page := newWorkspacePage("service logs").Parse(configRuntimeLogsHTML)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		query, ok := parseServiceLogPageQuery(r.URL.RawQuery)
		if !ok {
			http.Error(w, "请选择有效的受管实例。", http.StatusBadRequest)
			return
		}

		// The platform's bounded journal and service queries may exceed the normal page deadline.
		response := http.NewResponseController(w)
		_ = response.SetWriteDeadline(time.Time{})
		logs, err := readLogs(query.Instance, query.Telemetry)
		_ = response.SetWriteDeadline(time.Now().Add(configWebWriteTimeout))
		view := struct {
			serviceLogPageQuery
			Logs        string
			Lines       int
			Unavailable bool
			Accounts    []receiverProcessAccount
		}{serviceLogPageQuery: query, Lines: serviceLogViewerLines}
		status := http.StatusOK
		if err != nil {
			view.Unavailable, status = true, http.StatusBadGateway
		} else if query.Telemetry {
			view.Logs, view.Accounts = serviceTelemetryView(logs, query.Selected)
			if query.Selected.AppID != "" && !slices.Contains(view.Accounts, query.Selected) {
				view.Accounts = append(view.Accounts, query.Selected)
			}
		} else {
			view.Logs = logs
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}
