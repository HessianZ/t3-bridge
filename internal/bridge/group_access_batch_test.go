package bridge

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func setupGroupAccessBatch(t *testing.T) (*Engine, *Lark, *accessFixture, GroupAccess) {
	t.Helper()
	e, l, _, _, f := setupAccess(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	defaultMention := false
	if err := e.Store.update(func(st *State) error {
		st.GroupAccess = GroupAccess{
			Chats:          map[string]bool{"oc_first": false, "oc_prior": true},
			Mentions:       map[string]bool{"oc_first": true, "oc_prior": false},
			RequireMention: &defaultMention,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	groups, _ := e.Store.groupAccess()
	l.Config.access.publishGroups(groups)
	return e, l, f, groups
}

func groupAccessBatchPages(t *testing.T, f *accessFixture, pages ...string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/open-apis/im/v1/chats" {
			return false
		}
		page := int(calls.Add(1)) - 1
		if page >= len(pages) {
			t.Errorf("unexpected joined groups page %d", page+1)
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		query := r.URL.Query()
		if r.Method != http.MethodGet || query.Get("page_token") != []string{"", "next"}[page] ||
			query.Get("user_id_type") != "open_id" || query.Get("page_size") != "100" {
			t.Errorf("wrong SDK joined-group query on page %d", page+1)
		}
		io.WriteString(w, pages[page])
		return true
	}
	return &calls
}

func TestLocalGroupAccessBatchGrantsExactlySelectedJoinedGroups(t *testing.T) {
	e, l, f, before := setupGroupAccessBatch(t)
	calls := groupAccessBatchPages(t, f,
		`{"code":0,"data":{"items":[{"chat_id":"oc_first"},{"chat_id":"oc_unselected"}],"has_more":true,"page_token":"next"}}`,
		`{"code":0,"data":{"items":[{"chat_id":"oc_second"}],"has_more":false}}`,
	)
	writes := 0
	e.Store.writeState = func(path string, data []byte, createOnly bool) (bool, error) {
		writes++
		if calls.Load() != 2 || l.Config.groupAllowed("oc_first") || l.Config.groupAllowed("oc_second") {
			t.Error("batch was written before the joined list was complete or published before persistence")
		}
		return localfile.WritePrivateAtomic(path, data, createOnly)
	}
	view, err := e.LocalGroupAccess(t.Context(), GroupAccessRequest{ChatIDs: []string{"oc_first", "oc_second"}, Access: "allow"})
	if err != nil || writes != 1 || !view.JoinedAvailable {
		t.Fatalf("batch grant view = %+v, err = %v, writes = %d", view, err, writes)
	}
	disk, err := readStateSnapshot(e.Store.dir)
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.Chats = map[string]bool{"oc_first": true, "oc_second": true, "oc_prior": true}
	inMemory, _ := e.Store.groupAccess()
	if !reflect.DeepEqual(disk.GroupAccess, want) || !reflect.DeepEqual(inMemory, want) {
		t.Fatalf("batch grant state on disk = %+v, in memory = %+v, want %+v", disk.GroupAccess, inMemory, want)
	}
	if !l.Config.groupAllowed("oc_first") || !l.Config.groupAllowed("oc_second") ||
		!l.Config.groupAllowed("oc_prior") || l.Config.groupAllowed("oc_unselected") ||
		!l.Config.requireMention("oc_first") || l.Config.requireMention("oc_second") || view.DefaultRequiresMention {
		t.Fatalf("receiver ACL or mention rules differ after batch grant: %+v", view)
	}
	joined := map[string]bool{}
	for _, group := range view.Groups {
		if group.Joined {
			joined[group.ID] = group.Allowed
		}
	}
	wantJoined := map[string]bool{"oc_first": true, "oc_second": true, "oc_unselected": false}
	if !reflect.DeepEqual(joined, wantJoined) {
		t.Fatalf("returned joined groups = %v, want %v", joined, wantJoined)
	}
}

func TestLocalGroupAccessBatchRejectsWithoutPublishing(t *testing.T) {
	const firstOnly = `{"code":0,"data":{"items":[{"chat_id":"oc_first"}],"has_more":false}}`
	const bothWithMore = `{"code":0,"data":{"items":[{"chat_id":"oc_first"},{"chat_id":"oc_second"}],"has_more":true,"page_token":"next"}}`
	const bothComplete = `{"code":0,"data":{"items":[{"chat_id":"oc_first"},{"chat_id":"oc_second"}],"has_more":false}}`
	for _, tc := range []struct {
		name       string
		pages      []string
		configure  func(*Engine, *Lark, *GroupAccessRequest)
		writeFails bool
	}{
		{name: "unjoined selection", pages: []string{firstOnly}},
		{name: "later page error", pages: []string{bothWithMore, `{"code":999,"msg":"synthetic list error"}`}},
		{name: "incomplete pagination", pages: []string{bothWithMore, bothWithMore}},
		{name: "no injected Lark", configure: func(e *Engine, _ *Lark, _ *GroupAccessRequest) { e.Lark = nil }},
		{name: "wrong state account", configure: func(e *Engine, _ *Lark, _ *GroupAccessRequest) { e.Config.AppID = "other-app" }},
		{name: "wrong Lark app", configure: func(_ *Engine, l *Lark, _ *GroupAccessRequest) { l.Config.AppID = "other-app" }},
		{name: "wrong Lark store", configure: func(_ *Engine, l *Lark, _ *GroupAccessRequest) { l.Store = nil }},
		{name: "empty batch", configure: func(_ *Engine, _ *Lark, r *GroupAccessRequest) { r.ChatIDs = r.ChatIDs[:0] }},
		{name: "duplicate IDs", configure: func(_ *Engine, _ *Lark, r *GroupAccessRequest) { r.ChatIDs[1] = r.ChatIDs[0] }},
		{name: "invalid chat ID", configure: func(_ *Engine, _ *Lark, r *GroupAccessRequest) { r.ChatIDs[1] = "oc_" }},
		{name: "non-grant access", configure: func(_ *Engine, _ *Lark, r *GroupAccessRequest) { r.Access = "deny" }},
		{name: "mixed single chat ID", configure: func(_ *Engine, _ *Lark, r *GroupAccessRequest) { r.ChatID = "oc_first" }},
		{name: "mixed mention mode", configure: func(_ *Engine, _ *Lark, r *GroupAccessRequest) { r.Mention = "off" }},
		{name: "write failure", pages: []string{bothComplete}, writeFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, l, f, before := setupGroupAccessBatch(t)
			calls := groupAccessBatchPages(t, f, tc.pages...)
			request := GroupAccessRequest{ChatIDs: []string{"oc_first", "oc_second"}, Access: "allow"}
			if tc.configure != nil {
				tc.configure(e, l, &request)
			}
			writeErr := errors.New("synthetic state write error")
			writes := 0
			e.Store.writeState = func(string, []byte, bool) (bool, error) {
				writes++
				return false, writeErr
			}
			if _, err := e.LocalGroupAccess(t.Context(), request); err == nil || tc.writeFails != errors.Is(err, writeErr) {
				t.Fatalf("unexpected batch grant error: %v", err)
			}
			wantWrites := 0
			if tc.writeFails {
				wantWrites = 1
			}
			if writes != wantWrites || int(calls.Load()) != len(tc.pages) {
				t.Fatalf("got %d writes and %d joined list calls, want %d and %d", writes, calls.Load(), wantWrites, len(tc.pages))
			}
			disk, err := readStateSnapshot(e.Store.dir)
			if err != nil {
				t.Fatal(err)
			}
			inMemory, _ := e.Store.groupAccess()
			if !reflect.DeepEqual(disk.GroupAccess, before) || !reflect.DeepEqual(inMemory, before) ||
				!reflect.DeepEqual(l.Config.access.groups, before) {
				t.Fatalf("failed batch changed group policy: on disk %+v, in store %+v, published %+v", disk.GroupAccess, inMemory, l.Config.access.groups)
			}
		})
	}
}

func TestValidateBatchGrantSizeBounds(t *testing.T) {
	ids := make([]string, 33)
	for i := range ids {
		ids[i] = fmt.Sprintf("oc_chat_%02d", i)
	}
	for _, size := range []int{0, 1, 32, 33} {
		request := GroupAccessRequest{ChatIDs: ids[:size], Access: "allow"}
		if err := request.ValidateBatchGrant(); (err == nil) != (size >= 1 && size <= 32) {
			t.Errorf("batch validation for %d chat IDs: %v", size, err)
		}
	}
}
