package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

const (
	windowsTestBinary   = `C:\桥接 & 测试\bridge.exe`
	windowsTestConfig   = `C:\用户 A\O'Brien & 测试\config.json`
	windowsTestRecovery = "<RestartOnFailure><Interval>PT1M</Interval><Count>3</Count></RestartOnFailure>"
)

func TestWindowsServiceDefinition(t *testing.T) {
	service := windowsService{sid: "S-1-5-21-111-222-333-1001", name: "work"}
	path := "\\" + service.taskName()
	definition := service.render(windowsTestBinary, windowsTestConfig)
	if strings.Contains(definition, "<RestartOnFailure") || !strings.Contains(definition, "<Enabled>false</Enabled>") {
		t.Fatal("install definition must be disabled with no Scheduler retry policy")
	}
	if got := service.taskKind(path, definition); got != windowsTaskWithSupervisor {
		t.Fatalf("rendered task kind = %v, want %v", got, windowsTaskWithSupervisor)
	}
	exported := strings.NewReplacer(
		"?>", " encoding=\"UTF-16\"?>",
		"<Enabled>false</Enabled>", "<Enabled>true</Enabled>",
	).Replace(definition)
	if got := service.taskKind(path, exported); got != windowsTaskWithSupervisor {
		t.Fatalf("exported task kind = %v, want %v", got, windowsTaskWithSupervisor)
	}
	for name, policy := range map[string]string{
		"native retries": windowsTestRecovery,
		"empty policy":   "<RestartOnFailure/>",
	} {
		t.Run(name, func(t *testing.T) {
			if got := service.taskKind(path, windowsTestTaskDefinition(service, service.supervisorTaskArgumentPrefix(), policy)); got != windowsTaskUnmanaged {
				t.Fatalf("supervised task with native policy has kind %v, want %v", got, windowsTaskUnmanaged)
			}
		})
	}
	var parsed windowsTaskDefinition
	if err := xml.Unmarshal([]byte(definition), &parsed); err != nil {
		t.Fatal(err)
	}
	action := parsed.Actions.Entries[0]
	argv, err := windows.DecomposeCommandLine(`"` + action.Command + `" ` + action.Arguments)
	if err != nil || len(argv) != 6 || argv[0] != windowsTestBinary || argv[1] != "run" || argv[2] != "-windows-service-supervisor" ||
		argv[3] != service.name || argv[4] != "-config" || argv[5] != windowsTestConfig {
		t.Fatalf("receiver arguments = %q, %v", argv, err)
	}
	argv[5] = "relative.json"
	if err := checkEntry(t, argv[1:]...); err == nil || !strings.Contains(err.Error(), "managed Windows receiver requires an absolute JSON configuration path") {
		t.Fatalf("rendered receiver flag did not reach Windows dispatch: %v", err)
	}

	for name, edit := range map[string][2]string{
		"marker":     {serviceMarker, "External task: "},
		"config":     {"config.json", "changed.json"},
		"instance":   {windowsServiceSupervisorFlag + " work", windowsServiceSupervisorFlag + " other"},
		"binary":     {"bridge.exe", "other.exe"},
		"logon type": {"InteractiveToken", "Password"},
		"trigger":    {"<LogonTrigger><UserId>" + service.sid, "<LogonTrigger><UserId>S-1-5-18"},
		"principal":  {"\n      <UserId>" + service.sid, "\n      <UserId>S-1-5-18"},
		"run level":  {"LeastPrivilege", "HighestAvailable"},
		"extra exec": {"</Actions>", "<Exec><Command>other.exe</Command></Exec></Actions>"},
		"context":    {`Context="BridgeUser"`, `Context="OtherUser"`},
		"parallel":   {"IgnoreNew", "Parallel"},
	} {
		t.Run(name, func(t *testing.T) {
			if service.taskKind(path, strings.Replace(definition, edit[0], edit[1], 1)) != windowsTaskUnmanaged {
				t.Fatal("modified task was recognized as managed")
			}
		})
	}
	if service.taskKind(`\other`+path, definition) != windowsTaskUnmanaged {
		t.Fatal("task in another folder was recognized as managed")
	}
	for _, unsafePath := range []string{`C:\%TEMP%\config.json`, `C:\$(Arg1)\config.json`, `C:\config".json`} {
		if _, err := windowsServicePath(unsafePath, ".json"); err == nil {
			t.Fatalf("accepted path %q", unsafePath)
		}
	}
}

func TestWindowsServiceHistoricalDefinitions(t *testing.T) {
	service := windowsService{sid: "S-1-5-21-111-222-333-1001", name: "work"}
	path := "\\" + service.taskName()
	const logPrefix = `run -windows-service-instance work -config "`
	for _, tt := range []struct {
		name, prefix, policy string
		kind                 windowsTaskKind
	}{
		{"legacy", `run -config "`, "", windowsTaskLegacy},
		{"logs", logPrefix, "", windowsTaskWithLogs},
		{"recovery", logPrefix, windowsTestRecovery, windowsTaskWithRecovery},
		{"exported recovery", logPrefix, "<RestartOnFailure><Count>2</Count><Interval>PT60S</Interval></RestartOnFailure>", windowsTaskWithRecovery},
		{"missing interval", logPrefix, "<RestartOnFailure><Count>3</Count></RestartOnFailure>", windowsTaskWithLogs},
		{"missing count", logPrefix, "<RestartOnFailure><Interval>PT1M</Interval></RestartOnFailure>", windowsTaskWithLogs},
		{"zero count", logPrefix, strings.Replace(windowsTestRecovery, "<Count>3</Count>", "<Count>0</Count>", 1), windowsTaskWithLogs},
	} {
		t.Run(tt.name, func(t *testing.T) {
			definition := windowsTestTaskDefinition(service, tt.prefix, tt.policy)
			exported := strings.Replace(definition, "?>", " encoding=\"UTF-16\"?>", 1)
			if got := service.taskKind(path, exported); got != tt.kind {
				t.Fatalf("task kind = %v, want %v", got, tt.kind)
			}
		})
	}
}

func TestWindowsServiceLogsInspectDefinition(t *testing.T) {
	service := windowsService{sid: "S-1-5-21-111-222-333-1001", name: "work"}
	path := "\\" + service.taskName()
	legacy := windowsTestTaskDefinition(service, windowsLegacyTaskArgumentPrefix, "")
	for _, test := range []struct {
		tasks []windowsScheduledTask
		want  string
	}{
		{nil, "not installed"},
		{[]windowsScheduledTask{{Path: path, XML: "<Task/>"}}, "refusing to manage it"},
		{[]windowsScheduledTask{{Path: path, XML: legacy}}, "task predates log capture"},
	} {
		err := service.logs(1, func(request windowsSchedulerRequest) ([]windowsScheduledTask, error) {
			if request.Action != "inspect" || request.Name != service.taskName() {
				t.Fatalf("incorrect scheduler request: %+v", request)
			}
			return test.tasks, nil
		}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("logs error = %v, want %q", err, test.want)
		}
	}
	for _, definition := range []string{
		service.render(windowsTestBinary, windowsTestConfig),
		windowsTestTaskDefinition(service, service.logTaskArgumentPrefix(), ""),
		windowsTestTaskDefinition(service, service.logTaskArgumentPrefix(), windowsTestRecovery),
	} {
		dir, err := service.managedLogDir(func(windowsSchedulerRequest) ([]windowsScheduledTask, error) {
			return []windowsScheduledTask{{Path: path, XML: definition}}, nil
		})
		if err != nil || filepath.Base(dir) != service.name {
			t.Fatalf("managed log directory = %q, error %v", dir, err)
		}
	}
}

func TestWindowsServiceManagementChecksOwnership(t *testing.T) {
	service := windowsService{sid: "S-1-5-21-111-222-333-1001", name: "work"}
	definition := service.render(windowsTestBinary, windowsTestConfig)
	for _, action := range []string{"start", "stop", "restart", "unregister"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			foreign := func(request windowsSchedulerRequest) ([]windowsScheduledTask, error) {
				calls++
				if request.Action != "inspect" {
					t.Fatalf("attempted %q for an external task", request.Action)
				}
				return []windowsScheduledTask{{Path: "\\" + service.taskName(), XML: "<Task/>"}}, nil
			}
			if err := service.manage(action, foreign, io.Discard); err == nil || calls != 1 {
				t.Fatalf("manage returned %v after %d scheduler calls", err, calls)
			}

			var output bytes.Buffer
			scheduler := func(request windowsSchedulerRequest) ([]windowsScheduledTask, error) {
				if request.Action == "inspect" {
					return []windowsScheduledTask{{Path: "\\" + service.taskName(), XML: definition}}, nil
				}
				if request.Action != action || request.Name != service.taskName() || request.XML != definition {
					t.Fatalf("incorrect scheduler request: %+v", request)
				}
				return nil, nil
			}
			if err := service.manage(action, scheduler, &output); err != nil {
				t.Fatal(err)
			}
			if action != "start" && !strings.Contains(output.String(), windowsServiceStopNote) {
				t.Fatalf("missing stop limitation in %q", output.String())
			}
		})
	}
	unavailable := errors.New("access denied")
	if err := service.manage("status", func(windowsSchedulerRequest) ([]windowsScheduledTask, error) {
		return nil, unavailable
	}, io.Discard); !errors.Is(err, unavailable) {
		t.Fatalf("status conflated a scheduler error with missing task: %v", err)
	}
}

func TestWindowsServiceListFiltersByOwnerAndDefinition(t *testing.T) {
	service := windowsService{sid: "S-1-5-21-111-222-333-1001"}
	managed := windowsService{sid: service.sid, name: "work"}
	older := windowsService{sid: service.sid, name: "older"}
	native := windowsService{sid: service.sid, name: "native"}
	external := windowsService{sid: service.sid, name: "external"}
	otherUser := windowsService{sid: "S-1-5-21-111-222-333-1002", name: "other"}
	var output bytes.Buffer
	err := service.list(func(request windowsSchedulerRequest) ([]windowsScheduledTask, error) {
		if request.Action != "list" || request.Name != service.taskPrefix() {
			t.Fatalf("unexpected scheduler request: %+v", request)
		}
		return []windowsScheduledTask{
			{Path: "\\" + otherUser.taskName(), XML: otherUser.render(windowsTestBinary, windowsTestConfig)},
			{Path: "\\" + external.taskName(), XML: managed.render(windowsTestBinary, windowsTestConfig)},
			{Path: "\\" + managed.taskName(), XML: managed.render(windowsTestBinary, windowsTestConfig), State: 4, Enabled: true},
			{Path: "\\" + older.taskName(), XML: windowsTestTaskDefinition(older, older.logTaskArgumentPrefix(), ""), State: 1},
			{Path: "\\" + native.taskName(), XML: windowsTestTaskDefinition(native, native.logTaskArgumentPrefix(), windowsTestRecovery), State: 3, Enabled: true},
		}, nil
	}, &output)
	want := "native: ready; Scheduler retry configured; logon autostart enabled\n" +
		"older: disabled; manual recovery; logon autostart disabled\n" +
		"work: running; receiver retry configured; logon autostart enabled\n"
	if err != nil || output.String() != want {
		t.Fatalf("list output %q, error %v", output.String(), err)
	}
}

func TestWindowsServiceInstallConfirmsSupervisor(t *testing.T) {
	service := windowsService{sid: "S-1-5-21-111-222-333-1001", name: "work"}
	definition := service.render(windowsTestBinary, windowsTestConfig)
	path := "\\" + service.taskName()
	for _, tt := range []struct {
		name   string
		task   windowsScheduledTask
		accept bool
	}{
		{"registered with supervisor", windowsScheduledTask{Path: path, XML: definition}, true},
		{"historical logs", windowsScheduledTask{Path: path, XML: windowsTestTaskDefinition(service, service.logTaskArgumentPrefix(), "")}, false},
		{"historical native retry", windowsScheduledTask{Path: path, XML: windowsTestTaskDefinition(service, service.logTaskArgumentPrefix(), windowsTestRecovery)}, false},
		{"stacked retry", windowsScheduledTask{Path: path, XML: windowsTestTaskDefinition(service, service.supervisorTaskArgumentPrefix(), windowsTestRecovery)}, false},
		{"unexpectedly enabled", windowsScheduledTask{Path: path, XML: definition, Enabled: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			err := service.install(definition, func(request windowsSchedulerRequest) ([]windowsScheduledTask, error) {
				if request.Action != "install" || request.Name != service.taskName() || request.SID != service.sid || request.XML != definition {
					t.Fatalf("unexpected Scheduler request: %+v", request)
				}
				return []windowsScheduledTask{tt.task}, nil
			}, &output)
			if tt.accept {
				if err != nil || !strings.Contains(output.String(), "logon autostart and receiver retries") {
					t.Fatalf("install output %q, error %v", &output, err)
				}
			} else if err == nil || output.Len() != 0 {
				t.Fatalf("install confirmed an unexpected task: output %q, error %v", &output, err)
			}
		})
	}
}

func windowsTestTaskDefinition(service windowsService, prefix, retryPolicy string) string {
	currentArgs := service.supervisorTaskArgumentPrefix() + windowsTestConfig + `"`
	fixtureArgs := prefix + windowsTestConfig + `"`
	dir := filepath.Dir(windowsTestConfig)
	return strings.NewReplacer(
		html.EscapeString(currentArgs), html.EscapeString(fixtureArgs),
		service.marker(windowsTestBinary, currentArgs, dir), service.marker(windowsTestBinary, fixtureArgs, dir),
		"</Settings>", retryPolicy+"</Settings>",
	).Replace(service.render(windowsTestBinary, windowsTestConfig))
}
