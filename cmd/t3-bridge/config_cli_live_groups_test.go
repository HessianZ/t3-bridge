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

func TestConfigLiveGroupsCommand(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	base := []string{"-process", id, "-app-id", "cli_example"}
	view := bridge.GroupAccessView{
		DefaultRequiresMention: true,
		JoinedAvailable:        true,
		MessageScope:           bridge.GroupMessageScopeGranted,
		Groups:                 []bridge.GroupAccessEntry{{ID: "oc_example", Allowed: true, Joined: true}, {ID: "oc_example_other"}},
	}
	for _, tc := range []struct {
		name, messageEvent string
		args               []string
		tenant             string
		req                bridge.GroupAccessRequest
		groups             []bridge.GroupAccessEntry
	}{
		{name: "query", tenant: "feishu", messageEvent: "subscribed"},
		{name: "old worker query", tenant: "feishu"},
		{name: "exact query", args: []string{"-chat-id", "oc_example"}, tenant: "feishu", messageEvent: "missing", groups: view.Groups[:1]},
		{name: "unknown group", args: []string{"-chat-id", "oc_missing"}, tenant: "feishu", messageEvent: "unpublished", groups: []bridge.GroupAccessEntry{}},
		{name: "app default", args: []string{"-tenant", "lark", "-mention", "off"}, tenant: "lark", messageEvent: "unknown", req: bridge.GroupAccessRequest{Mention: "off"}},
		{name: "group", args: []string{"-chat-id", "oc_example", "-access", "deny", "-mention", "inherit"}, tenant: "feishu", req: bridge.GroupAccessRequest{ChatID: "oc_example", Access: "deny", Mention: "inherit"}},
		{name: "confirmed batch", args: []string{"-chat-id", "oc_example", "-chat-id", "oc_other", "-access", "allow", "-confirm"}, tenant: "feishu", req: bridge.GroupAccessRequest{ChatIDs: []string{"oc_example", "oc_other"}, Access: "allow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := view
			reply.MessageEvent = tc.messageEvent
			var output bytes.Buffer
			err := runConfigLiveGroups(append(base, tc.args...), &output, func(ctx context.Context, process, tenant, appID string, req bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
				if _, ok := ctx.Deadline(); !ok || process != id || tenant != tc.tenant || appID != "cli_example" || !reflect.DeepEqual(req, tc.req) {
					t.Fatalf("wrong request to %s %s/%s: %+v", process, tenant, appID, req)
				}
				return reply, nil
			})
			var got bridge.GroupAccessView
			want := reply
			if tc.groups != nil {
				want.Groups = tc.groups
			}
			if err != nil || json.Unmarshal(output.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) || !strings.Contains(output.String(), `"messageScope"`) ||
				strings.Contains(output.String(), `"messageEvent":`) != (tc.messageEvent != "") {
				t.Fatalf("unexpected output %q, error %v", &output, err)
			}
		})
	}
	var help bytes.Buffer
	if err := runConfig([]string{"live-groups", "-help"}, nil, &help); err != nil || !strings.Contains(help.String(), "-process") {
		t.Fatalf("config did not expose live-groups help: %v, %s", err, &help)
	}
}

func TestConfigLiveGroupsRejectsInvalidFlags(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	base := []string{"-process", id, "-app-id", "cli_example"}
	for _, args := range [][]string{
		{"-process", id}, {"-process", id[:8], "-app-id", "cli_example"},
		append(base, "-tenant", "unknown"), append(base, "-access", "allow"),
		append(base, "-chat-id", " oc_example"), append(base, "-chat-id", "", "-mention", "off"),
		append(base, "-chat-id", "oc_example", "-access", "enable"), append(base, "-mention", "enable"),
		append(base, "-chat-id", "oc_example", "-chat-id", "oc_other", "-access", "allow"),
		append(base, "-chat-id", "oc_example", "-chat-id", "oc_example", "-access", "allow", "-confirm"),
		append(base, "-chat-id", "oc_example", "-chat-id", "other", "-access", "allow", "-confirm"),
		append(base, "-chat-id", "oc_example", "-chat-id", "oc_other", "-access", "deny", "-confirm"),
		append(base, "-chat-id", "oc_example", "-chat-id", "oc_other", "-access", "allow", "-mention", "off", "-confirm"),
	} {
		var output bytes.Buffer
		err := runConfigLiveGroups(args, &output, func(context.Context, string, string, string, bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
			t.Fatalf("invalid flags reached the receiver: %v", args)
			return bridge.GroupAccessView{}, nil
		})
		if err == nil {
			t.Errorf("invalid flags were accepted: %v", args)
		}
	}
}
