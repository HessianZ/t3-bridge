package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestConfigLiveAccessCommand(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	base := []string{"-process", id, "-app-id", "cli_example"}
	view := bridge.LocalAccessPolicyView{
		OwnerID: "ou_owner", RouteIDs: []string{"Work.Team"},
		Entries: []bridge.LocalAccessPolicyEntry{{Kind: "route", RouteID: "Work.Team", UserID: "ou_member", Configured: true, Override: "deny"}},
	}
	for _, tc := range []struct {
		name   string
		args   []string
		tenant string
		req    bridge.LocalAccessPolicyRequest
	}{
		{name: "read", tenant: "feishu"},
		{name: "user", tenant: "feishu",
			args: []string{"-kind", "user", "-user-id", "ou_member", "-mode", "allow"},
			req:  bridge.LocalAccessPolicyRequest{Kind: "user", UserID: "ou_member", Mode: "allow"}},
		{name: "admin", tenant: "lark",
			args: []string{"-tenant", "lark", "-kind", "admin", "-user-id", "ou_member", "-mode", "inherit"},
			req:  bridge.LocalAccessPolicyRequest{Kind: "admin", UserID: "ou_member", Mode: "inherit"}},
		{name: "route", tenant: "feishu",
			args: []string{"-kind", "route", "-user-id", "ou_member", "-route-id", "Work.Team", "-mode", "deny"},
			req:  bridge.LocalAccessPolicyRequest{Kind: "route", UserID: "ou_member", RouteID: "Work.Team", Mode: "deny"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runConfigLiveAccess(append(base, tc.args...), &output, func(ctx context.Context, process, tenant, appID string, req bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
				if _, timed := ctx.Deadline(); !timed || process != id || tenant != tc.tenant || appID != "cli_example" || req != tc.req {
					t.Fatalf("wrong request to %s %s/%s: %+v", process, tenant, appID, req)
				}
				return view, nil
			})
			var got bridge.LocalAccessPolicyView
			if err != nil || json.Unmarshal(output.Bytes(), &got) != nil || !reflect.DeepEqual(got, view) {
				t.Fatalf("unexpected output %q, error %v", &output, err)
			}
		})
	}
	var help bytes.Buffer
	if err := runConfig([]string{"live-access", "-help"}, nil, &help); err != nil || !strings.Contains(help.String(), "-route-id") {
		t.Fatalf("config did not expose live-access help: %v, %s", err, &help)
	}
}

func TestConfigLiveAccessRejectsIncompleteChanges(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	base := []string{"-process", id, "-app-id", "cli_example"}
	for _, args := range [][]string{
		{"-process", id}, {"-process", id[:8], "-app-id", "cli_example"},
		append(base, "-tenant", "unknown"), append(base, "-kind", ""), append(base, "-mode", "allow"),
		append(base, "-kind", "route", "-user-id", "ou_member", "-mode", "deny"),
		append(base, "-kind", "user", "-user-id", "ou_member", "-mode", "deny", "-route-id", ""),
		append(base, "-kind", "admin", "-user-id", "member", "-mode", "inherit"),
		append(base, "-kind", "admin", "-user-id", "ou_member", "-mode", "disable"),
	} {
		var output bytes.Buffer
		err := runConfigLiveAccess(args, &output, func(context.Context, string, string, string, bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
			t.Fatalf("invalid flags reached the receiver: %v", args)
			return bridge.LocalAccessPolicyView{}, nil
		})
		if err == nil {
			t.Errorf("invalid flags were accepted: %v", args)
		}
	}
}
