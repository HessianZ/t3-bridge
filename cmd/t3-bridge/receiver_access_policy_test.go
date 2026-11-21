package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestReceiverAccessPolicyReply(t *testing.T) {
	for _, test := range []struct {
		name string
		view bridge.LocalAccessPolicyView
		want error
	}{
		{name: "empty ACL and owner", view: bridge.LocalAccessPolicyView{Entries: []bridge.LocalAccessPolicyEntry{}}},
		{name: "null entries", want: errReceiverAccessPolicyUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, process := testGroupAccessRegistry(t)
			account := newReceiverGroupAccount("lark", "cli_shared")
			pending := receiverAccessPolicyIPC.pendingPath(process.dir, account)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			type result struct {
				view bridge.LocalAccessPolicyView
				err  error
			}
			done := make(chan result, 1)
			go func() {
				view, err := r.accessPolicy(ctx, process.process.ID, account.Tenant, account.AppID, bridge.LocalAccessPolicyRequest{})
				done <- result{view, err}
			}()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case got := <-done:
					if !errors.Is(got.err, test.want) {
						t.Fatalf("access policy returned %v, want %v", got.err, test.want)
					}
					if got.err == nil && (got.view.OwnerID != "" || got.view.Entries == nil || len(got.view.Entries) != 0) {
						t.Fatalf("expected an empty ACL and owner, got %+v", got.view)
					}
					return
				case <-ticker.C:
					receiverAccessPolicyIPC.serve(ctx, process, account, pending, func(context.Context, bridge.LocalAccessPolicyRequest) (bridge.LocalAccessPolicyView, error) {
						return test.view, nil
					})
				case <-ctx.Done():
					t.Fatal("access policy exchange did not complete")
				}
			}
		})
	}
}

func TestReceiverAccessPolicyRejectLegacyRegistration(t *testing.T) {
	r, process := testGroupAccessRegistry(t)
	legacy := process.process
	legacy.AccessPolicy = false
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"accessPolicy"`) {
		t.Fatal("legacy fixture must omit the capability")
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(process.dir, "process.json"), data, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.accessPolicy(t.Context(), legacy.ID, "", "cli_shared", bridge.LocalAccessPolicyRequest{}); !errors.Is(err, errReceiverAccessPolicyUnsupported) {
		t.Fatalf("legacy receiver returned %v", err)
	}
}
