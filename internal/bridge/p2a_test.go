package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
)

func roleCommand(t *testing.T, e *Engine, ch *accessChannel, id, actor, text, target string) {
	t.Helper()
	n := groupMessage(id, actor, "chat", text, true)
	if target != "" {
		n.Mentions = []channel.Mention{{Key: "@person", OpenID: target, Name: "synthetic member"}}
	}
	receiveAccess(t, ch, n)
	tick(t, e)
}
func TestP2aDynamicPermissionsPersistAndDoNotDriftOwner(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	receiveAccess(t, ch, groupMessage("owner-task", "user", "chat", "/nas 讨论", true))
	tick(t, e)
	before := onlyTask(t, e)
	roleCommand(t, e, ch, "add-user", "user", "/invite user @person", "new-user")
	roleCommand(t, e, ch, "add-admin", "user", "/invite admin @person", "new-admin")
	if !e.Config.isAdmin("new-admin") || e.Config.TargetAllowed(Normalize(groupMessage("x", "new-admin", "chat", "", true)), e.Config.Routes["nas"], "nas") {
		t.Fatal("admin inherited execution rights")
	}
	roleCommand(t, e, ch, "no-owner-delete", "new-admin", "/remove user @person", "user")
	roleCommand(t, e, ch, "no-owner-demote", "new-admin", "/remove admin @person", "user")
	if !e.Config.isAdmin("user") || !e.Config.userAllowed("user") {
		t.Fatal("owner removed")
	}
	roleCommand(t, e, ch, "grant-route", "new-admin", "/invite route nas @person", "new-user")
	m := Normalize(groupMessage("check", "new-user", "chat", "", true))
	if !e.Config.TargetAllowed(m, e.Config.Routes["nas"], "nas") || e.Config.TargetAllowed(m, e.Config.Routes["mac"], "mac") {
		t.Fatal("route grant escaped target")
	}
	after := e.Store.snapshot().Tasks[before.ID]
	if after.ThreadID != before.ThreadID || after.RequestID != before.RequestID || !taskTargetAllowed(e.Config, after) || targetIdentity(before.Target) != targetIdentity(after.Target) || x.sends != 1 {
		t.Fatal("adding another user drifted owner execution")
	}
	roleCommand(t, e, ch, "revoke-admin", "user", "/remove admin @person", "new-admin")
	roleCommand(t, e, ch, "unauthorized-mutation", "new-admin", "/invite route mac @person", "new-user")
	if e.Config.isAdmin("new-admin") || e.Config.TargetAllowed(m, e.Config.Routes["mac"], "mac") {
		t.Fatal("revoked admin still mutated permissions")
	}
	roleCommand(t, e, ch, "deny-user", "user", "/remove user @person", "new-user")
	if e.Config.Allowed(m) || e.Config.TargetAllowed(m, e.Config.Routes["nas"], "nas") {
		t.Fatal("global revoke did not override route")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	e.Config.access = &accessRuntime{owner: "user", policy: s.snapshot().AccessPolicy, groups: s.snapshot().GroupAccess}
	if e.Config.TargetAllowed(m, e.Config.Routes["nas"], "nas") || e.Config.isAdmin("new-admin") {
		t.Fatal("revocations lost on restart")
	}
	if strings.Contains(strings.Join(f.replies, ""), "synthetic-secret") {
		t.Fatal("management leaked secrets")
	}
}
func TestP2aRevocationFreezesQueuedAndRechecksBeforeSend(t *testing.T) {
	e, _, ch, x, _ := setupAccess(t)
	receiveAccess(t, ch, groupMessage("first", "other", "chat", "/nas 首轮", true))
	tick(t, e)
	receiveAccess(t, ch, groupMessage("queued", "other", "chat", "后续输入", true))
	tick(t, e)
	roleCommand(t, e, ch, "deny-route", "user", "/remove route nas @person", "other")
	roleCommand(t, e, ch, "restore-route", "user", "/invite route nas @person", "other")
	x.observation.Status = "completed"
	tick(t, e)
	tick(t, e)
	if x.sends != 1 {
		t.Fatal("restoring route revived revoked queue")
	}
	// A permission change after Preflight but before Send must leave only an idle thread.
	e2, _, ch2, x2, _ := setupAccess(t)
	x2.onPreflight = func() {
		e2.Store.update(func(st *State) error {
			st.AccessPolicy.Routes = map[string]map[string]bool{"nas": {"other": false}}
			return nil
		})
		e2.publishPolicy()
	}
	receiveAccess(t, ch2, groupMessage("late-revoke", "other", "chat", "/nas 检查", true))
	tick(t, e2)
	if x2.sends != 0 || onlyTask(t, e2).Phase != "blocked" {
		t.Fatal("revocation between checks dispatched")
	}
}
func TestP2aSettingsBudgetProgressAndExplicitTimeout(t *testing.T) {
	e, _, ch, x, _ := setupAccess(t)
	now := time.Now().UTC().Add(time.Hour)
	e.Now = func() time.Time { return now.Add(time.Second) }
	roleCommand(t, e, ch, "budget", "user", "/config concurrency 1", "")
	receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas 首个任务", true))
	tick(t, e)
	receiveAccess(t, ch, groupMessage("two", "other", "chat", "/nas 第二用户任务", true))
	tick(t, e)
	if x.sends != 1 {
		t.Fatal("concurrency budget not enforced")
	}
	var first, second *Task
	for _, v := range e.Store.snapshot().Tasks {
		if v.Source.UserID == "user" {
			first = v
		} else {
			second = v
		}
	}
	if second.Phase != "waiting_capacity" || second.ThreadID != "" || first.DeadlineAt != "" {
		t.Fatal("capacity confused with maintenance or silently added timeout")
	}
	roleCommand(t, e, ch, "progress-off", "user", "/config progress off", "")
	x.observation.Progress = "assistant visible details"
	tick(t, e)
	if strings.Contains(e.Store.snapshot().Tasks[first.ID].Summary, "visible details") {
		t.Fatal("progress toggle ignored")
	}
	roleCommand(t, e, ch, "timeout", "user", "/timeout 2", "")
	task := e.Store.snapshot().Tasks[first.ID]
	if task.TimeoutSeconds != 2 || task.DeadlineAt == "" {
		t.Fatal("explicit timeout not applied")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	if s.runSettings().MaxConcurrent != 1 || s.runSettings().Progress == nil || *s.runSettings().Progress {
		t.Fatal("settings lost on restart")
	}
	now = now.Add(3 * time.Second)
	tick(t, e)
	task = e.Store.snapshot().Tasks[first.ID]
	if !task.TimeoutExpired || task.Phase != "stopped" || task.NeedsReconcile || x.stops != 1 {
		t.Fatal("deadline did not stop original run")
	}
	// Capacity frees only after original task has stopped; no reset/replay of the old request.
	tick(t, e)
	if x.sends != 2 {
		t.Fatal("capacity did not release")
	}
	tick(t, e)
	if x.stops != 1 {
		t.Fatal("timeout stop retried")
	}
}
func TestP2aUnknownSendTimeoutDoesNotStopUnrelatedRun(t *testing.T) {
	e, x, _, _ := testEngine(t)
	now := time.Now()
	e.Now = func() time.Time { return now }
	x.sendErr = errors.New("unknown send")
	e.Store.update(func(st *State) error {
		st.Timeouts = map[string]int{sessionKey(message("x", ""), "nas", e.Config.Routes["nas"]): 1}
		return nil
	})
	accept(t, e, message("one", "/nas 原请求"))
	tick(t, e)
	x.observation = Observation{FoundRequest: false}
	now = now.Add(2 * time.Second)
	tick(t, e)
	if x.sends != 1 || x.stops != 0 || onlyTask(t, e).Phase != "stopped" || !strings.Contains(onlyTask(t, e).Summary, "待核对") {
		t.Fatal("unknown send replayed or unrelated run stopped")
	}
}

func TestP2aUncertainStopKeepsCapacityAcrossRestart(t *testing.T) {
	for _, mode := range []string{"unknown-send-timeout", "stop-error", "stop-fence-crash"} {
		t.Run(mode, func(t *testing.T) {
			e, _, ch, x, _ := setupAccess(t)
			e.Config.DefaultRoute = "nas"
			now := time.Now().UTC().Add(time.Hour)
			e.Now = func() time.Time { return now }
			roleCommand(t, e, ch, "budget", "user", "/config concurrency 1", "")
			if mode == "unknown-send-timeout" {
				roleCommand(t, e, ch, "timeout", "user", "/timeout 1", "")
				x.sendErr = errors.New("unknown send")
			}
			receiveAccess(t, ch, groupMessage("one", "user", "chat", "/nas 首个任务", true))
			tick(t, e)
			first := onlyTask(t, e)
			wantStops := 0
			switch mode {
			case "unknown-send-timeout":
				x.observation = Observation{FoundRequest: false}
				now = now.Add(2 * time.Second)
				tick(t, e)
			case "stop-error":
				x.stopErr = errors.New("stop outcome unknown")
				roleCommand(t, e, ch, "stop", "user", "/stop", "")
				wantStops = 1
			case "stop-fence-crash":
				if err := e.Store.update(func(st *State) error {
					task := st.Tasks[first.ID]
					task.StopRequested, task.StopSent, task.NeedsReconcile = true, true, true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			x.sendErr, x.stopErr = nil, nil
			e.Store.Close()
			s, err := OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e.Store = s
			// Model a fresh receiver accepting into the reopened Store.
			m := Normalize(groupMessage("two-reopened", "other", "chat", "/nas 第二用户任务", true))
			if err := s.Accept(e.Config, m); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				tick(t, e)
			}
			st := s.snapshot()
			uncertain := st.Tasks[first.ID]
			if x.sends != 1 || x.stops != wantStops || x.opens != 1 || !uncertain.NeedsReconcile || uncertain.Phase != "stopped" || !strings.Contains(uncertain.Summary, "占用并发预算") {
				t.Fatal("uncertain stop/send released capacity or retried", mode)
			}
			for id, task := range st.Tasks {
				if id != first.ID && (task.Phase != "waiting_capacity" || task.ThreadID != "") {
					t.Fatal("second task escaped the unresolved capacity hold")
				}
			}
		})
	}
}

func TestP2aConfirmedStopReleasesReconciledSendCapacity(t *testing.T) {
	e, x, _, _ := testEngine(t)
	now := time.Now().UTC()
	e.Now = func() time.Time { return now }
	if err := e.Store.update(func(st *State) error {
		st.Settings.MaxConcurrent = 1
		st.Timeouts = map[string]int{sessionKey(message("x", ""), "nas", e.Config.Routes["nas"]): 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	x.sendErr = errors.New("send response lost")
	accept(t, e, message("one", "/nas 原任务"))
	tick(t, e)
	x.observation = Observation{FoundRequest: true, RunID: "owned-run", Status: "running"}
	now = now.Add(2 * time.Second)
	tick(t, e)
	if task := onlyTask(t, e); task.NeedsReconcile || task.Phase != "stopped" || x.stops != 1 {
		t.Fatal("confirmed original-run stop retained uncertain capacity")
	}
	x.sendErr = nil
	m := message("two", "/nas 第二用户任务")
	m.UserID = "other"
	accept(t, e, m)
	tick(t, e)
	if x.sends != 2 || x.stops != 1 {
		t.Fatal("confirmed stop failed to release capacity or repeated stop")
	}
}
func TestP2aToolContextIsTargetScopedAndDoesNotDrift(t *testing.T) {
	e, x, _, _ := testEngine(t)
	target := e.Config.Routes["nas"]
	identity := targetIdentity(target)
	target.ToolContext = &ToolContext{CLIPath: "/home/synthetic/bin/lark-cli", Version: "1.0.96", Identity: "bot", EnvironmentID: target.EnvironmentID, Verified: true}
	e.Config.Routes["nas"] = target
	if targetIdentity(target) != identity {
		t.Fatal("tool display metadata drifted execution identity")
	}
	accept(t, e, message("nas", "/nas ## 原始Markdown\n**保留**"))
	tick(t, e)
	if !strings.HasPrefix(x.request, "/nas ## 原始Markdown\n**保留**") || !strings.Contains(x.request, target.ToolContext.CLIPath) || !strings.Contains(x.request, "operator_open_id: user") || !strings.Contains(x.request, "chat_id: chat") || !strings.Contains(x.request, "topic_id: topic") || !strings.Contains(x.request, "CLI，不是MCP") || !strings.Contains(x.request, "不借Mac") {
		t.Fatal("controlled tool context/original identity missing", x.request)
	}
	x.observation.Status = "completed"
	tick(t, e)
	accept(t, e, message("mac", "/mac 只解释"))
	tick(t, e)
	if strings.Contains(x.request, "/home/synthetic/") {
		t.Fatal("NAS tool path leaked into Mac route")
	}
	bad := *target.ToolContext
	bad.EnvironmentID = "other"
	if bad.Validate(target.EnvironmentID) == nil {
		t.Fatal("cross-environment tool accepted")
	}
}
func TestP2aReviewIsNewReadOnlyContextAndKeepsDSDefault(t *testing.T) {
	e, x, l, _ := testEngine(t)
	e.Config.DefaultRoute = "nas"
	review := e.Config.Routes["nas"]
	review.Model = "codebuddy-direct/hy4free"
	review.Options = map[string]any{"variant": "high"}
	e.Config.Routes["nas-review"] = review
	e.Config.ReviewRoute = "nas-review"
	accept(t, e, message("ds", "/nas 请讨论方案"))
	tick(t, e)
	ds := onlyTask(t, e)
	x.observation.Status = "completed"
	x.observation.Summary = "旧轮资料 secret=private"
	tick(t, e)
	accept(t, e, message("second", "/nas 另一轮"))
	tick(t, e)
	tick(t, e)
	m := message("review", "/review 请复核旧轮")
	m.ParentID = ds.CardID
	accept(t, e, m)
	tick(t, e)
	st := e.Store.snapshot()
	var r *Task
	for _, v := range st.Tasks {
		if v.RouteID == "nas-review" {
			r = v
		}
	}
	if x.opens != 2 {
		t.Fatal("review reused an existing DS thread")
	}
	if r == nil || r.ID == ds.ID || r.Target.Model != review.Model || r.DispatchSource.InputKind != "review" || strings.Contains(r.DispatchSource.Quote, "secret=private") || !strings.Contains(r.DispatchSource.Quote, shortTaskID(ds.RequestID)) || st.Selected[userScope(m)] != "nas" {
		t.Fatal("review redirected DS or lost bounded old-round source")
	}
	if !strings.Contains(prompt(r), "仅获只读讨论授权") {
		t.Fatal("review expanded production authorization")
	}
	if !strings.Contains(l.replies[len(l.replies)-1], "独立") {
		t.Fatal("missing review acknowledgement")
	}
}
func TestP2aDrainAndAccountIdentity(t *testing.T) {
	e, x, _, _ := testEngine(t)
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.BindAccount("another-app"); err == nil {
		t.Fatal("account switched over original state")
	}
	if err := e.Store.DrainCheck(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	accept(t, e, message("one", "/nas task"))
	tick(t, e)
	if err := e.Store.DrainCheck(e.Config.AppID); err == nil {
		t.Fatal("active state declared drained")
	}
	x.observation.Status = "completed"
	x.observation.Summary = "done"
	tick(t, e)
	if err := e.Store.DrainCheck(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(e.Config.StateDir, "state.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("account state not private")
	}
	before := e.Store.snapshot()
	e.Store.DrainCheck(e.Config.AppID)
	if !reflect.DeepEqual(before, e.Store.snapshot()) {
		t.Fatal("drain check wrote state")
	}
}
func TestP2aDoctorAndReconnectControlAreLowRiskIdempotent(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	calls := 0
	e.Reconnect = func(context.Context) (string, error) { calls++; return "本Bridge连接已替换", nil }
	e.ConnectionStatus = func() string { return "synthetic connection status" }
	roleCommand(t, e, ch, "doctor", "other", "/doctor", "")
	if x.sends != 0 || x.preflights != 0 || !strings.Contains(f.replies[len(f.replies)-1], "synthetic connection status") || strings.Contains(f.replies[len(f.replies)-1], ".secret") {
		t.Fatal("doctor probed secrets/model or omitted status")
	}
	roleCommand(t, e, ch, "not-admin", "other", "/reconnect", "")
	if calls != 0 {
		t.Fatal("ordinary member reconnected")
	}
	f.replyFail = true
	roleCommand(t, e, ch, "admin-reconnect", "user", "/reconnect", "")
	f.replyFail = false
	tick(t, e)
	tick(t, e)
	if calls != 1 || x.sends != 0 {
		t.Fatal("reconnect acknowledgement retried side effect")
	}
	b, _ := json.Marshal(e.Store.snapshot().Commands)
	if strings.Contains(string(b), "synthetic-secret") {
		t.Fatal("command persisted secret")
	}
}
