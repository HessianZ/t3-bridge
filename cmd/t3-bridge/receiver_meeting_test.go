package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestReceiverMeetingClaimsOnlySelectedAccount(t *testing.T) {
	registry, process := testGroupAccessRegistry(t)
	account := newReceiverGroupAccount("lark", "cli_shared")
	other := newReceiverGroupAccount("feishu", "cli_shared")
	request := bridge.LocalMeetingRequest{Action: "join", Number: "123456789"}
	command := receiverIPCCommand[bridge.LocalMeetingRequest]{
		receiverGroupRoute: receiverGroupRoute{Instance: process.process.ID, Account: account, Ticket: strings.Repeat("a", 32)},
		Expires:            time.Now().Add(5 * time.Second),
		Request:            &request,
	}
	ctx := t.Context()
	if err := receiverMeetingIPC.enqueue(ctx, registry, command); err != nil {
		t.Fatal(err)
	}
	var calls []bridge.LocalMeetingRequest
	apply := func(_ context.Context, got bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error) {
		calls = append(calls, got)
		return bridge.LocalMeetingView{Enabled: true, Available: true, Meetings: []bridge.LocalMeetingEntry{}, Outcome: "pending"}, nil
	}
	receiverMeetingIPC.serve(ctx, process, other, receiverMeetingIPC.pendingPath(process.dir, other), apply)
	if len(calls) != 0 {
		t.Fatal("another tenant claimed the selected App's meeting request")
	}
	pending := receiverMeetingIPC.pendingPath(process.dir, account)
	receiverMeetingIPC.serve(ctx, process, account, pending, apply)
	receiverMeetingIPC.serve(ctx, process, account, pending, apply)
	if len(calls) != 1 || calls[0] != request {
		t.Fatalf("meeting requests = %+v", calls)
	}
	path := filepath.Join(receiverMeetingIPC.returnDir(process.dir, command.Ticket), "reply.json")
	reply, err := readReceiverIPCFile[receiverIPCReply[bridge.LocalMeetingView]](path, receiverIPCCommandLimit, true)
	if err != nil || reply.receiverGroupRoute != command.receiverGroupRoute || reply.Failed || reply.View == nil || reply.View.Outcome != "pending" {
		t.Fatalf("meeting receipt = %+v, %v", reply, err)
	}
}

func TestConfigLiveMeetingsRequiresConfirmationAndPrintsPending(t *testing.T) {
	process := strings.Repeat("a", 32)
	args := []string{"-process", process, "-tenant", "lark", "-app-id", "cli_selected"}
	var out bytes.Buffer
	var requests []bridge.LocalMeetingRequest
	apply := func(_ context.Context, id, tenant, app string, request bridge.LocalMeetingRequest) (bridge.LocalMeetingView, error) {
		if id != process || tenant != "lark" || app != "cli_selected" {
			t.Fatalf("unexpected target: %q %q %q", id, tenant, app)
		}
		requests = append(requests, request)
		view := bridge.LocalMeetingView{Enabled: true, Available: true, Meetings: []bridge.LocalMeetingEntry{}}
		if request.Action == "join" {
			view.Outcome = "pending"
		}
		return view, nil
	}
	if err := runConfigLiveMeetings(args, &out, apply); err != nil || len(requests) != 1 || requests[0].Action != "status" {
		t.Fatalf("status: %v, requests %+v", err, requests)
	}
	out.Reset()
	join := append(args, "-action", "join", "-number", "123 456 789")
	if err := runConfigLiveMeetings(join, &out, apply); err == nil || len(requests) != 1 {
		t.Fatalf("unconfirmed join: %v, requests %+v", err, requests)
	}
	if err := runConfigLiveMeetings(append(join, "-confirm"), &out, apply); err == nil || len(requests) != 2 ||
		requests[1] != (bridge.LocalMeetingRequest{Action: "join", Number: "123456789"}) || !strings.Contains(out.String(), `"outcome":"pending"`) {
		t.Fatalf("ambiguous join: %v, requests %+v, output %s", err, requests, &out)
	}
}
