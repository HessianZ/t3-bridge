package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
	"golang.org/x/sys/windows"
)

const windowsServiceStopNote = "Task Scheduler can forcibly terminate the receiver. T3 work and drain state were not checked."

func init() {
	readHostServiceInventory = func() ([]serviceInventoryEntry, error) {
		sid, err := windowsServiceUser()
		if err != nil {
			return nil, err
		}
		return (windowsService{sid: sid}).inventory(runWindowsScheduler)
	}
}

func runWindowsService(args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
		_, err := fmt.Fprintln(output, serviceUsage)
		return err
	}
	if len(args) == 0 {
		return errors.New(serviceUsage)
	}
	action := args[0]
	switch action {
	case "render", "install", "status", "start", "stop", "restart", "unregister", "list", "logs":
	default:
		return errors.New(serviceUsage)
	}
	flags := flag.NewFlagSet("t3-bridge service "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	var name, binary, config, profile string
	var logs serviceLogOptions
	if action != "list" {
		flags.StringVar(&name, "instance", "", "instance name (lowercase letters, digits and hyphens)")
	}
	if action == "logs" {
		logs.flags(flags)
	}
	if action == "render" || action == "install" {
		flags.StringVar(&binary, "binary", "", "path to the bridge executable (defaults to this executable)")
		flags.StringVar(&config, "config", "", "path to the private JSON configuration")
		flags.StringVar(&profile, "profile", "", "use the named configuration profile")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(serviceUsage)
	}
	if action != "list" && !serviceInstanceName.MatchString(name) {
		return errors.New("-instance must contain 1–48 lowercase letters, digits or hyphens and begin with a letter or digit")
	}
	if action == "logs" {
		if err := logs.validate(); err != nil {
			return err
		}
	}
	sid, err := windowsServiceUser()
	if err != nil {
		return err
	}
	service := windowsService{sid: sid, name: name}
	switch action {
	case "list":
		return service.list(runWindowsScheduler, output)
	case "logs":
		if logs.follow {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return service.followTelemetry(ctx, logs.lines, runWindowsScheduler, output, logs.account)
		}
		if logs.telemetry {
			return exportServiceTelemetry(output, logs.account, func(raw io.Writer) error {
				return service.logs(logs.lines, runWindowsScheduler, raw)
			})
		}
		return service.logs(logs.lines, runWindowsScheduler, output)
	case "render", "install":
		if binary == "" {
			binary, err = os.Executable()
			if err != nil {
				return err
			}
		}
		binary, err = windowsServicePath(binary, ".exe")
		if err != nil {
			return fmt.Errorf("-binary: %w", err)
		}
		config, err = resolveConfigPath(config, profile, defaultConfigPath)
		if err != nil {
			return err
		}
		config, err = windowsServicePath(config, ".json")
		if err != nil {
			return fmt.Errorf("-config: %w", err)
		}
		definition := service.render(binary, config)
		if action == "render" {
			_, err := io.WriteString(output, definition)
			return err
		}
		file, err := localfile.OpenRegular(binary)
		if err != nil {
			return fmt.Errorf("-binary must point to a regular executable file: %w", err)
		}
		if err := file.Close(); err != nil {
			return err
		}
		raw, err := bridge.ReadPrivateConfigJSON(config)
		if err != nil {
			return fmt.Errorf("-config must point to a readable private JSON file: %w", err)
		}
		if _, err := bridge.ParseConfig(raw); err != nil {
			return err
		}
		return service.install(definition, runWindowsScheduler, output)
	default:
		return service.manage(action, runWindowsScheduler, output)
	}
}

func windowsServiceUser() (string, error) {
	interactive, err := windows.CreateWellKnownSid(windows.WinInteractiveSid)
	if err != nil {
		return "", err
	}
	// CheckTokenMembership with a null handle uses the calling security context.
	member, err := windows.Token(0).IsMember(interactive)
	if err != nil {
		return "", err
	}
	token := windows.GetCurrentProcessToken()
	if !member || token.IsElevated() {
		return "", errors.New("run Windows service management in a non-elevated session as the interactive Windows user")
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

type windowsSchedulerRequest struct {
	Action string
	Name   string
	SID    string
	XML    string
}

type windowsScheduledTask struct {
	Path    string
	XML     string
	State   int
	Enabled bool
}

type windowsScheduler func(windowsSchedulerRequest) ([]windowsScheduledTask, error)

func (s windowsService) install(definition string, scheduler windowsScheduler, output io.Writer) error {
	name := s.taskName()
	tasks, err := scheduler(windowsSchedulerRequest{Action: "install", Name: name, SID: s.sid, XML: definition})
	if err != nil {
		return err
	}
	if len(tasks) != 1 || tasks[0].Enabled || s.taskKind(tasks[0].Path, tasks[0].XML) != windowsTaskWithSupervisor {
		return fmt.Errorf("%s: Scheduler returned an unexpected definition after registration", name)
	}
	_, err = fmt.Fprintf(output, "%s: installed, disabled; use service start to enable logon autostart and receiver retries\n", name)
	return err
}

func (s windowsService) inspect(scheduler windowsScheduler) (*windowsScheduledTask, windowsTaskKind, error) {
	name := s.taskName()
	tasks, err := scheduler(windowsSchedulerRequest{Action: "inspect", Name: name})
	if err != nil {
		return nil, windowsTaskUnmanaged, err
	}
	if len(tasks) == 0 {
		return nil, windowsTaskUnmanaged, nil
	}
	task := &tasks[0]
	kind := s.taskKind(task.Path, task.XML)
	if len(tasks) != 1 || kind == windowsTaskUnmanaged {
		return nil, windowsTaskUnmanaged, fmt.Errorf("%s: task identity or action differs from the managed definition; refusing to manage it", name)
	}
	return task, kind, nil
}

func (s windowsService) manage(action string, scheduler windowsScheduler, output io.Writer) error {
	name := s.taskName()
	task, kind, err := s.inspect(scheduler)
	if err != nil {
		return err
	}
	if task == nil {
		if action == "status" {
			_, err := fmt.Fprintf(output, "%s: not installed\n", name)
			return err
		}
		return fmt.Errorf("%s: not installed", name)
	}
	if action == "status" {
		_, err := fmt.Fprintf(output, "%s: %s\n", name, task.status(kind))
		return err
	}
	if action != "start" {
		if _, err := fmt.Fprintln(output, windowsServiceStopNote); err != nil {
			return err
		}
	}
	if _, err := scheduler(windowsSchedulerRequest{Action: action, Name: name, XML: task.XML}); err != nil {
		return err
	}
	message := "start requested; logon autostart enabled"
	switch action {
	case "stop":
		message = "Scheduler task stopped; logon autostart and failure recovery disabled"
	case "restart":
		message = "previous Scheduler instance stopped; start requested and logon autostart enabled"
	case "unregister":
		message = "Scheduler task unregistered"
	}
	_, err = fmt.Fprintf(output, "%s: %s\n", name, message)
	return err
}

func (s windowsService) list(scheduler windowsScheduler, output io.Writer) error {
	entries, err := s.inventory(scheduler)
	if err != nil {
		return err
	}
	return writeServiceInventory(entries, output)
}

func (s windowsService) inventory(scheduler windowsScheduler) ([]serviceInventoryEntry, error) {
	prefix := s.taskPrefix()
	tasks, err := scheduler(windowsSchedulerRequest{Action: "list", Name: prefix})
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Path < tasks[j].Path })
	var inventory []serviceInventoryEntry
	for _, task := range tasks {
		name, ok := strings.CutPrefix(task.Path, "\\"+prefix)
		if !ok || !serviceInstanceName.MatchString(name) {
			continue
		}
		instance := windowsService{sid: s.sid, name: name}
		kind := instance.taskKind(task.Path, task.XML)
		if kind == windowsTaskUnmanaged {
			continue
		}
		inventory = append(inventory, serviceInventoryEntry{
			name:   name,
			status: task.status(kind),
			manage: func(action string) error { return instance.manage(action, scheduler, io.Discard) },
		})
	}
	return inventory, nil
}

func (t windowsScheduledTask) status(kind windowsTaskKind) string {
	state := "unknown"
	switch t.State {
	case 1:
		state = "disabled"
	case 2:
		state = "queued"
	case 3:
		state = "ready"
	case 4:
		state = "running"
	}
	recovery := "manual recovery"
	switch kind {
	case windowsTaskWithRecovery:
		recovery = "Scheduler retry configured"
	case windowsTaskWithSupervisor:
		recovery = "receiver retry configured"
	}
	autostart := "disabled"
	if t.Enabled {
		autostart = "enabled"
	}
	return state + "; " + recovery + "; logon autostart " + autostart
}

func runWindowsScheduler(request windowsSchedulerRequest) ([]windowsScheduledTask, error) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	// PowerShell's -EncodedCommand expects UTF-16LE. Only the fixed script is code.
	script := make([]byte, 0, 2*len(windowsSchedulerScript))
	for _, word := range utf16.Encode([]rune(windowsSchedulerScript)) {
		script = append(script, byte(word), byte(word>>8))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, command, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(payload))
	raw, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Task Scheduler %s: %w (operation outcome unknown)", request.Action, ctx.Err())
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("Task Scheduler %s: %q", request.Action, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("Task Scheduler %s: %w", request.Action, err)
	}
	var tasks []windowsScheduledTask
	if err := json.Unmarshal(raw, &tasks); err != nil {
		return nil, fmt.Errorf("Task Scheduler %s: invalid response: %w", request.Action, err)
	}
	return tasks, nil
}

const windowsSchedulerScript = `
$ErrorActionPreference = 'Stop'
try {
    [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
    $request = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd())) | ConvertFrom-Json
    $scheduler = New-Object -ComObject 'Schedule.Service'
    $scheduler.Connect()
    $folder = $scheduler.GetFolder('\')
    $result = @()

    function Get-TaskSnapshot($task) {
        @{ Path = [string]$task.Path; XML = [string]$task.Xml; State = [int]$task.State; Enabled = [bool]$task.Enabled }
    }

    function Test-TaskActive($task) {
        $state = [int]$task.State
        if ($state -lt 1 -or $state -gt 4) { throw 'Scheduler reported an unknown task state.' }
        $state -eq 2 -or $state -eq 4 -or $task.GetInstances(0).Count -gt 0
    }

    if ($request.Action -eq 'list') {
        $result = @(foreach ($task in $folder.GetTasks(1)) {
            if ($task.Name.StartsWith($request.Name, [StringComparison]::Ordinal)) { Get-TaskSnapshot $task }
        })
    } elseif ($request.Action -eq 'install') {
        # TASK_CREATE, TASK_LOGON_INTERACTIVE_TOKEN. An existing task is never overwritten.
        $task = $folder.RegisterTask($request.Name, $request.XML, 2, $request.SID, $null, 3, $null)
        $result = @(Get-TaskSnapshot $task)
    } else {
        $task = $null
        try {
            $task = $folder.GetTask($request.Name)
        } catch {
            # HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND)
            if ($_.Exception.GetBaseException().HResult -ne -2147024894) { throw }
        }
        if ($request.Action -eq 'inspect') {
            if ($null -ne $task) { $result = @(Get-TaskSnapshot $task) }
        } else {
            if ($null -eq $task -or $task.Path -cne ('\' + $request.Name) -or $task.Xml -cne $request.XML) {
                throw 'Task definition changed since inspection.'
            }
            if ($request.Action -in 'stop', 'restart', 'unregister') {
                $task.Enabled = $false
                if ($task.Enabled) { throw 'Scheduler task could not be disabled.' }
                if (Test-TaskActive $task) { $task.Stop(0) }
                $deadline = [Diagnostics.Stopwatch]::StartNew()
                while (Test-TaskActive $task) {
                    if ($deadline.Elapsed.TotalSeconds -ge 10) { throw 'Scheduler task is still active after stop.' }
                    Start-Sleep -Milliseconds 200
                }
            }
            if ($request.Action -eq 'unregister') {
                $folder.DeleteTask($request.Name, 0)
            }
            if ($request.Action -in 'start', 'restart') {
                $task.Enabled = $true
                if (-not (Test-TaskActive $task)) { $null = $task.Run($null) }
            }
        }
    }
    [Console]::Out.WriteLine((ConvertTo-Json -InputObject $result -Compress))
} catch {
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}
`
