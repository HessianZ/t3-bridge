package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic poll work on a frozen release, without providers, network or real state.
// This measures retained-history cost; it is not a NAS stress-test result.
func BenchmarkReplacementRetainedPoll(b *testing.B) {
	for _, count := range []int{8, 32, 64} {
		b.Run(fmt.Sprintf("tasks-%d", count), func(b *testing.B) {
			s, err := OpenStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			body := strings.Repeat("synthetic answer", 512)
			if err := s.update(func(st *State) error {
				for i := 0; i < count; i++ {
					id := fmt.Sprintf("synthetic-%d", i)
					t := &Task{ID: id, Phase: "completed", Summary: "completed", RequestID: id, CardID: "card-" + id, Result: body}
					for j := 0; j < 4; j++ {
						t.Replies = append(t.Replies, ReplyRound{RequestID: fmt.Sprintf("past-%d", j), Phase: "completed", Result: body[:len(body)/2]})
					}
					t.NotifyHash = stable(t.Phase, t.Summary, t.Link, t.RequestID, t.ResultIssue, t.Result)
					st.Tasks[id] = t
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(s.dir, "state.json"))
			if err != nil {
				b.Fatal(err)
			}
			e := &Engine{Store: s}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := e.Tick(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(info.Size()), "state-bytes")
		})
	}
}
