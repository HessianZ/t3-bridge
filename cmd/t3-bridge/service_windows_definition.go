//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

const windowsTaskSchema = "http://schemas.microsoft.com/windows/2004/02/mit/task"

type windowsService struct {
	sid  string
	name string
}

func windowsServicePath(path, extension string) (string, error) {
	absolute, err := serviceAbsolutePath(path)
	if err != nil {
		return "", err
	}
	// Scheduler can expand %VAR% and $(ArgN) even in a direct Exec action.
	if strings.ContainsAny(absolute, `%"`) || strings.Contains(absolute, "$(") {
		return "", errors.New("task path contains quotes or Task Scheduler substitution characters")
	}
	if !filepath.IsAbs(absolute) || !strings.EqualFold(filepath.Ext(absolute), extension) {
		return "", fmt.Errorf("expected an absolute %s file path", extension)
	}
	return absolute, nil
}

func (s windowsService) taskPrefix() string {
	return "t3-bridge-managed-" + s.sid + "-"
}

func (s windowsService) taskName() string {
	return s.taskPrefix() + s.name
}

func (s windowsService) marker(binary, arguments, directory string) string {
	name := s.taskName()
	// Bind the marker to the installed action, including the resolved profile path.
	action := strings.Join([]string{name, binary, arguments, directory}, "\x00")
	return fmt.Sprintf("%s%s (sha256:%x)", serviceMarker, name, sha256.Sum256([]byte(action)))
}

const windowsLegacyTaskArgumentPrefix = `run -config "`

type windowsTaskKind uint8

const (
	windowsTaskUnmanaged windowsTaskKind = iota
	windowsTaskLegacy
	windowsTaskWithLogs
	windowsTaskWithRecovery
	windowsTaskWithSupervisor
)

func (s windowsService) logTaskArgumentPrefix() string {
	return "run " + windowsServiceInstanceFlag + " " + s.name + ` -config "`
}

func (s windowsService) supervisorTaskArgumentPrefix() string {
	return "run " + windowsServiceSupervisorFlag + " " + s.name + ` -config "`
}

func (s windowsService) render(binary, config string) string {
	arguments := s.supervisorTaskArgumentPrefix() + config + `"`
	directory := filepath.Dir(config)
	return fmt.Sprintf(`<?xml version="1.0"?>
<Task xmlns="%s">
  <RegistrationInfo>
    <URI>\%s</URI>
    <Description>%s</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger><UserId>%s</UserId></LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="BridgeUser">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>false</Enabled>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
  </Settings>
  <Actions Context="BridgeUser">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
      <WorkingDirectory>%s</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, windowsTaskSchema, s.taskName(), s.marker(binary, arguments, directory), s.sid, s.sid,
		html.EscapeString(binary), html.EscapeString(arguments), html.EscapeString(directory))
}

type windowsTaskDefinition struct {
	XMLName      xml.Name
	Registration struct {
		URI         string
		Description string
	} `xml:"RegistrationInfo"`
	Principals []struct {
		ID                 string `xml:"id,attr"`
		UserID             string `xml:"UserId"`
		LogonType          string
		RunLevel           string
		GroupID            string `xml:"GroupId"`
		RequiredPrivileges *struct{}
	} `xml:"Principals>Principal"`
	Triggers struct {
		Entries []struct {
			XMLName xml.Name
			UserID  string `xml:"UserId"`
			Enabled *bool
		} `xml:",any"`
	}
	Actions struct {
		Context string `xml:"Context,attr"`
		Entries []struct {
			XMLName          xml.Name
			Command          string
			Arguments        string
			WorkingDirectory string
		} `xml:",any"`
	}
	Settings struct {
		MultipleInstancesPolicy string
		AllowStartOnDemand      *bool
		RestartOnFailure        *struct {
			Interval string
			Count    string
		}
	}
}

func (s windowsService) taskKind(path, taskXML string) windowsTaskKind {
	if path != "\\"+s.taskName() {
		return windowsTaskUnmanaged
	}
	var definition windowsTaskDefinition
	decoder := xml.NewDecoder(strings.NewReader(taskXML))
	// RegisteredTask.Xml is a Unicode BSTR. JSON has already converted it to UTF-8,
	// even if the XML declaration still says UTF-16.
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "utf-16") {
			return input, nil
		}
		return nil, fmt.Errorf("unexpected task XML encoding %q", charset)
	}
	if err := decoder.Decode(&definition); err != nil {
		return windowsTaskUnmanaged
	}
	if definition.XMLName != (xml.Name{Space: windowsTaskSchema, Local: "Task"}) ||
		definition.Registration.URI != path || len(definition.Principals) != 1 ||
		len(definition.Triggers.Entries) != 1 || len(definition.Actions.Entries) != 1 {
		return windowsTaskUnmanaged
	}
	principal := definition.Principals[0]
	if principal.ID == "" || definition.Actions.Context != principal.ID ||
		principal.LogonType != "InteractiveToken" ||
		(principal.RunLevel != "" && principal.RunLevel != "LeastPrivilege") ||
		principal.GroupID != "" || principal.RequiredPrivileges != nil || !s.isAccount(principal.UserID) {
		return windowsTaskUnmanaged
	}
	trigger := definition.Triggers.Entries[0]
	if trigger.XMLName != (xml.Name{Space: windowsTaskSchema, Local: "LogonTrigger"}) ||
		(trigger.Enabled != nil && !*trigger.Enabled) || !s.isAccount(trigger.UserID) {
		return windowsTaskUnmanaged
	}
	settings := definition.Settings
	if (settings.MultipleInstancesPolicy != "" && settings.MultipleInstancesPolicy != "IgnoreNew") ||
		(settings.AllowStartOnDemand != nil && !*settings.AllowStartOnDemand) {
		return windowsTaskUnmanaged
	}
	action := definition.Actions.Entries[0]
	if action.XMLName != (xml.Name{Space: windowsTaskSchema, Local: "Exec"}) ||
		definition.Registration.Description != s.marker(action.Command, action.Arguments, action.WorkingDirectory) {
		return windowsTaskUnmanaged
	}
	binary, err := windowsServicePath(action.Command, ".exe")
	if err != nil || binary != action.Command {
		return windowsTaskUnmanaged
	}
	kind := windowsTaskWithSupervisor
	config, prefix := strings.CutPrefix(action.Arguments, s.supervisorTaskArgumentPrefix())
	if !prefix {
		kind = windowsTaskWithLogs
		config, prefix = strings.CutPrefix(action.Arguments, s.logTaskArgumentPrefix())
		if !prefix {
			kind = windowsTaskLegacy
			config, prefix = strings.CutPrefix(action.Arguments, windowsLegacyTaskArgumentPrefix)
		}
	}
	config, suffix := strings.CutSuffix(config, `"`)
	if !prefix || !suffix {
		return windowsTaskUnmanaged
	}
	absolute, err := windowsServicePath(config, ".json")
	if err != nil || absolute != config || action.WorkingDirectory != filepath.Dir(config) {
		return windowsTaskUnmanaged
	}
	recovery := settings.RestartOnFailure
	if kind == windowsTaskWithSupervisor && recovery != nil {
		return windowsTaskUnmanaged
	}
	if kind == windowsTaskWithLogs && recovery != nil {
		count, err := strconv.ParseUint(strings.TrimSpace(recovery.Count), 10, 8)
		if err == nil && count > 0 && strings.TrimSpace(recovery.Interval) != "" {
			return windowsTaskWithRecovery
		}
	}
	return kind
}

func (s windowsService) isAccount(account string) bool {
	if account == s.sid {
		return true
	}
	if account == "" || strings.HasPrefix(account, "S-") {
		return false
	}
	// The Scheduler can export a Windows account name in place of its SID.
	sid, _, _, err := windows.LookupSID("", account)
	return err == nil && sid.String() == s.sid
}
