package bridge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Whole-state cloning used to scale every 100ms idle wake with all retained answers.
func idleEngine(t testing.TB, history bool) *Engine {
	now := time.Now()
	st := State{Tasks: map[string]*Task{}}
	for i := 0; i < 8; i++ {
		task := &Task{ID: fmt.Sprint(i), Phase: []string{"completed", "clarify", "blocked", "stopped", "failed"}[i%5]}
		if task.Phase == "stopped" || task.Phase == "failed" {
			task.StopRequested = true
		}
		if history {
			task.Result = strings.Repeat("x", maxResultBytes)
			for j := 0; j < 2; j++ {
				task.Replies = append(task.Replies, ReplyRound{Result: strings.Repeat("y", 64<<10)})
			}
		}
		task.NotifyHash = stable(task.Phase, task.Summary, task.Link, task.RequestID, task.ResultIssue, task.Result)
		st.Tasks[task.ID] = task
	}
	return &Engine{Store: &Store{state: st, dir: t.TempDir()}, PollInterval: 2 * time.Second, nextPoll: now.Add(time.Hour)}
}
func TestIdleTickDoesNotAllocateForRetainedHistory(t *testing.T) {
	for _, history := range []bool{false, true} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			e := idleEngine(t, history)
			allocations := testing.AllocsPerRun(1, func() {
				if err := e.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
			if allocations != 0 {
				t.Fatalf("idle Tick allocated %.0f times; retained history must not be cloned or hashed", allocations)
			}
		})
	}
}
func BenchmarkIdleTick(b *testing.B) {
	for _, history := range []bool{false, true} {
		b.Run(fmt.Sprint(history), func(b *testing.B) {
			e := idleEngine(b, history)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := e.Tick(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func TestIdleGateKeepsInboxAndImmediateControls(t *testing.T) {
	for _, command := range []string{"/nas 新输入", "/stop", "/reset"} {
		t.Run(command, func(t *testing.T) {
			e, x, _, _ := testEngine(t)
			now := time.Now()
			e.Now = func() time.Time { return now }
			accept(t, e, message("first", "/nas 起始任务"))
			tick(t, e)
			if command == "/nas 新输入" {
				x.observation.Status = "completed"
				x.observation.Summary = "旧结果"
				tick(t, e)
			}
			e.PollInterval = 2 * time.Second
			e.nextPoll = now.Add(2 * time.Second)
			m := shortMessage("next", command, now)
			accept(t, e, m)
			tick(t, e)
			if command == "/nas 新输入" {
				if x.sends != 1 || len(e.Store.snapshot().Inbox) != 1 {
					t.Fatal("quiet window changed")
				}
				now = now.Add(quietWindow + time.Millisecond)
				tick(t, e)
				if x.sends != 2 || len(e.Store.snapshot().Inbox) != 0 {
					t.Fatal("new input starved before background poll")
				}
			} else if x.stops != 1 || onlyTask(t, e).Phase != "stopped" {
				t.Fatal("control waited for background poll", x.stops)
			}
		})
	}
}
func TestIdleGateKeepsFirstNotificationAndReadyTask(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			now := time.Now()
			e.Now = func() time.Time { return now }
			e.PollInterval = 2 * time.Second
			e.nextPoll = now.Add(2 * time.Second)
			d := Decision{Action: "clarify"}
			if ready {
				d = Decision{RouteID: "nas", Action: "new_task"}
			}
			if err := e.newTask(message("new", "/nas 新任务"), d, "等待处理"); err != nil {
				t.Fatal(err)
			}
			if ready {
				e.Store.update(func(st *State) error {
					for _, task := range st.Tasks {
						task.NotifyHash = "ready-was-notified"
						task.CardID = "ready-card"
					}
					return nil
				})
			}
			tick(t, e)
			if l.notifications != 1 || onlyTask(t, e).CardID == "" {
				t.Fatal("first pending notification starved")
			}
			if ready && x.sends != 1 {
				t.Fatal("durable ready task starved")
			}
		})
	}
}
func TestIdleGateKeepsBackgroundAndNotificationRetry(t *testing.T) {
	e, x, l, _ := testEngine(t)
	now := time.Now()
	e.Now = func() time.Time { return now }
	accept(t, e, message("first", "/nas 起始任务"))
	tick(t, e)
	e.PollInterval = 2 * time.Second
	e.nextPoll = now.Add(2 * time.Second)
	x.observation.Status = "completed"
	x.observation.Summary = "最终结果"
	tick(t, e)
	if onlyTask(t, e).Phase != "tracking" {
		t.Fatal("background ran before due")
	}
	l.fail = true
	now = now.Add(2 * time.Second)
	tick(t, e)
	task := onlyTask(t, e)
	attempts := l.notifications
	if task.NotificationIssue == "" || task.Phase != "completed" {
		t.Fatal("failed terminal notification not retained")
	}
	l.fail = false
	tick(t, e)
	if l.notifications != attempts {
		t.Fatal("idle gate failed to keep retry on background cadence")
	}
	now = now.Add(2 * time.Second)
	tick(t, e)
	task = onlyTask(t, e)
	if l.notifications != attempts+1 || task.NotificationIssue != "" || x.sends != 1 || !strings.Contains(l.sentCards[task.CardID], "最终结果") {
		t.Fatal("pending notification retry starved or execution replayed")
	}
}

func TestIdleGateFinishedStopFlagsDoNotKeepWorkerAwake(t *testing.T) {
	for _, phase := range []string{"completed", "ready", "failed", "stopped"} {
		t.Run(phase, func(t *testing.T) {
			e, x, l, _ := testEngine(t)
			now := time.Now()
			e.Now = func() time.Time { return now }
			if phase == "completed" {
				accept(t, e, message("one", "/nas 完成后停止"))
				tick(t, e)
				x.observation.Status = "completed"
				x.observation.Summary = "done"
				tick(t, e)
			} else if err := e.newTask(message("one", "/nas 停止"), Decision{RouteID: "nas", Action: "new_task"}, "等待"); err != nil {
				t.Fatal(err)
			}
			e.Store.update(func(st *State) error {
				for _, task := range st.Tasks {
					task.Phase = phase
					task.StopRequested = true
					task.StopSent = false
				}
				return nil
			})
			e.PollInterval = 2 * time.Second
			e.nextPoll = now.Add(2 * time.Second)
			sends := x.sends
			tick(t, e)
			task := onlyTask(t, e)
			want := phase
			if phase == "completed" || phase == "ready" {
				want = "stopped"
			}
			if task.Phase != want || task.StopSent || x.sends != sends || x.stops != 0 || task.NotifyHash == "" {
				t.Fatal("terminal stop/first notification not settled", phase, task.Phase)
			}
			notifications := l.notifications
			allocations := testing.AllocsPerRun(3, func() {
				if err := e.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
			if allocations != 0 || l.notifications != notifications {
				t.Fatal("terminal StopRequested kept idle worker awake", phase, allocations)
			}
		})
	}
}
