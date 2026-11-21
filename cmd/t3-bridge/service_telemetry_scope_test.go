package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	scopedTelemetryFeishu = `{"type":"receiver-event","tenant":"feishu","appId":"cli_scope","at":"2026-11-19T02:03:04Z","kind":"run-started"}` + "\n"
	scopedTelemetryLark   = `{"type":"receiver-event","tenant":"lark","appId":"cli_scope","at":"2026-11-19T02:03:04Z","kind":"run-completed"}` + "\n"
	scopedTelemetryPrefix = `{"type":"receiver-event","tenant":"feishu","appId":"cli_scope_extra","at":"2026-11-19T02:03:04Z","kind":"outbound-sent"}` + "\n"
	scopedTelemetrySecret = `{"type":"receiver-event","tenant":"feishu","appId":"cli_scope","at":"2026-11-19T02:03:04Z","kind":"run-started","error":"synthetic secret","fields":{"text":"synthetic message"}}` + "\n"
)

func TestServiceTelemetryScopeOptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"unscoped logs", nil, false},
		{"unscoped telemetry", []string{"-telemetry"}, false},
		{"feishu snapshot", []string{"-telemetry", "-tenant", "feishu", "-app-id", "cli_scope"}, false},
		{"lark follow", []string{"-telemetry", "-follow", "-lines", "0", "-tenant=lark", "-app-id=cli_scope"}, false},
		{"scope without telemetry", []string{"-tenant=feishu", "-app-id=cli_scope"}, true},
		{"missing tenant", []string{"-telemetry", "-app-id=cli_scope"}, true},
		{"missing app ID", []string{"-telemetry", "-tenant=feishu"}, true},
		{"empty tenant", []string{"-telemetry", "-tenant=", "-app-id=cli_scope"}, true},
		{"empty app ID", []string{"-telemetry", "-tenant=feishu", "-app-id="}, true},
		{"only empty tenant", []string{"-telemetry", "-tenant="}, true},
		{"only empty app ID", []string{"-telemetry", "-app-id="}, true},
		{"both explicitly empty", []string{"-telemetry", "-tenant=", "-app-id="}, true},
		{"copied blanks", []string{"-telemetry", "-tenant", " \t", "-app-id", "\u00a0"}, true},
		{"uppercase tenant", []string{"-telemetry", "-tenant=Feishu", "-app-id=cli_scope"}, true},
		{"padded tenant", []string{"-telemetry", "-tenant=feishu ", "-app-id=cli_scope"}, true},
		{"padded app ID", []string{"-telemetry", "-tenant=feishu", "-app-id= cli_scope\n"}, true},
		{"missing app prefix", []string{"-telemetry", "-tenant=feishu", "-app-id=scope"}, true},
		{"empty app suffix", []string{"-telemetry", "-tenant=feishu", "-app-id=cli_"}, true},
		{"invalid app character", []string{"-telemetry", "-tenant=feishu", "-app-id=cli_scope/extra"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var options serviceLogOptions
			flags := flag.NewFlagSet("service logs", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			options.flags(flags)
			err := flags.Parse(tc.args)
			if err == nil {
				err = options.validate()
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("args %q: error = %v, want error = %t", tc.args, err, tc.wantErr)
			}
		})
	}
}

func TestWriteServiceTelemetryScope(t *testing.T) {
	input := scopedTelemetryLark + scopedTelemetryPrefix + scopedTelemetrySecret
	selected := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	var output bytes.Buffer
	if err := writeServiceTelemetry(strings.NewReader(input), &output, selected); err != nil || output.String() != scopedTelemetryFeishu {
		t.Fatalf("scoped telemetry = %q, error = %v", output.String(), err)
	}

	failure := errors.New("write failed")
	writer := serviceTelemetryErrorWriter{failure}
	if err := writeServiceTelemetry(strings.NewReader(scopedTelemetryLark+scopedTelemetryPrefix), writer, selected); err != nil {
		t.Fatalf("excluded records returned write error: %v", err)
	}
	if err := writeServiceTelemetry(strings.NewReader(input), writer, selected); !errors.Is(err, failure) {
		t.Fatalf("matching record write error = %v, want %v", err, failure)
	}
}

func TestServiceTelemetryViewScope(t *testing.T) {
	lark := receiverProcessAccount{Tenant: "lark", AppID: "cli_scope"}
	feishu := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	prefix := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope_extra"}
	invalid := `{"type":"receiver-event","tenant":"lark","appId":"cli_invalid","at":"2026-11-19T02:03:04Z","kind":"private"}` + "\n"
	logs := "ordinary log\n" + invalid + scopedTelemetryPrefix + scopedTelemetryLark + scopedTelemetrySecret + scopedTelemetryLark
	for _, tc := range []struct {
		name    string
		account receiverProcessAccount
		body    string
	}{
		{"all", receiverProcessAccount{}, scopedTelemetryPrefix + scopedTelemetryLark + scopedTelemetryFeishu + scopedTelemetryLark},
		{"feishu", feishu, scopedTelemetryFeishu},
		{"lark", lark, scopedTelemetryLark + scopedTelemetryLark},
		{"absent", receiverProcessAccount{Tenant: "lark", AppID: "cli_absent"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, accounts := serviceTelemetryView(logs, tc.account)
			if body != tc.body {
				t.Fatalf("telemetry body = %q, want %q", body, tc.body)
			}
			if len(accounts) != 3 || !slices.Contains(accounts, feishu) || !slices.Contains(accounts, lark) || !slices.Contains(accounts, prefix) {
				t.Fatalf("snapshot accounts = %+v, want %+v", accounts, []receiverProcessAccount{feishu, lark, prefix})
			}
		})
	}
}

func TestFollowServiceTelemetryFilesScope(t *testing.T) {
	dir := t.TempDir()
	stdout := filepath.Join(dir, "stdout.log")
	stderr := filepath.Join(dir, "stderr.log")
	live := strings.Replace(scopedTelemetrySecret, "run-started", "run-completed", 1)
	appendTelemetryFollowLog(t, stdout, scopedTelemetryLark+scopedTelemetrySecret)
	appendTelemetryFollowLog(t, stderr, scopedTelemetryPrefix)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var polls int
	checkDir := func(string) error {
		polls++
		switch polls {
		case 2:
			appendTelemetryFollowLog(t, stderr, scopedTelemetryPrefix+scopedTelemetryLark+live)
		case 3:
			cancel()
			return os.ErrNotExist
		}
		return nil
	}

	var output bytes.Buffer
	selected := receiverProcessAccount{Tenant: "feishu", AppID: "cli_scope"}
	if err := followServiceTelemetryFiles(ctx, dir, 10, checkDir, &output, selected); err != nil {
		t.Fatal(err)
	}
	if want := scopedTelemetryFeishu + strings.Replace(scopedTelemetryFeishu, "run-started", "run-completed", 1); output.String() != want {
		t.Fatalf("scoped file stream = %q, want %q", output.String(), want)
	}
}
