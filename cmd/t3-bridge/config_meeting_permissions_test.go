package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func TestSavedMeetingPermissionsBindsAccountAndRevision(t *testing.T) {
	source := tuiReceiverEnableSource(t)
	revision, choices, err := savedMeetingPermissionChoices(source.Path)
	if err != nil || revision != source.Revision || len(choices) != 2 || choices[1] != (meetingPermissionChoice{AppID: "cli_additional", Tenant: "feishu"}) {
		t.Fatalf("saved choices = %+v, %v", choices, err)
	}
	calls := 0
	mutate := false
	check := func(ctx context.Context, account bridge.Config) (bridge.MeetingPermissionReport, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok || account.AppID != "cli_additional" || account.ReceiverAuthorized {
			t.Fatalf("unexpected probe target: %q or incorrect context", account.AppID)
		}
		if mutate {
			if err := os.WriteFile(source.Path, append(append([]byte(nil), source.JSON...), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return bridge.MeetingPermissionReport{AppID: account.AppID, Tenant: "feishu"}, nil
	}
	other := receiverProcessAccount{Tenant: "lark", AppID: choices[1].AppID}
	if _, err := verifySavedMeetingPermissions(t.Context(), source, other, check); err == nil || calls != 0 {
		t.Fatalf("cross-tenant probe reached the transport: calls=%d, %v", calls, err)
	}
	selected := receiverProcessAccount{Tenant: "feishu", AppID: choices[1].AppID}
	if got, err := verifySavedMeetingPermissions(t.Context(), source, selected, check); err != nil || got.AppID != selected.AppID || calls != 1 {
		t.Fatalf("matching saved App = %+v, %v", got, err)
	}
	mutate = true
	if got, err := verifySavedMeetingPermissions(t.Context(), source, selected, check); !errors.Is(err, errMeetingPermissionSourceChanged) || got.AppID != "" || calls != 2 {
		t.Fatalf("changed source returned a result: %+v, %v, calls=%d", got, err, calls)
	}
}
