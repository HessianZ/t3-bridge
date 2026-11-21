package bridge

import "testing"

func TestVerifiedThreadURL(t *testing.T) {
	for _, tc := range []struct {
		name, address, environment, thread string
		valid                              bool
	}{
		{"hosted", "https://app.t3.codes/threads/env/mcp%3Alaunch%3Aabc", "env", "mcp:launch:abc", true},
		{"custom", "https://t3.example.test:8443/threads/env/id%253Aoriginal", "env", "id%3Aoriginal", true},
		{"same IDs", "https://app.t3.codes/threads/env/env", "env", "env", true},
		{"wrong environment", "https://app.t3.codes/threads/id/id", "env", "id", false},
		{"wrong thread", "https://app.t3.codes/threads/env/other", "env", "id", false},
		{"nested path", "https://app.t3.codes/threads/env/id/extra", "env", "id/extra", false},
		{"no environment", "https://app.t3.codes/threads/env/id", "", "id", false},
		{"relative", "//app.t3.codes/threads/env/id", "env", "id", false},
		{"http", "http://app.t3.codes/threads/env/id", "env", "id", false},
		{"userinfo", "https://example.test@app.t3.codes/threads/env/id", "env", "id", false},
		{"query", "https://app.t3.codes/threads/env/id?synthetic-credential", "env", "id", false},
		{"fragment", "https://app.t3.codes/threads/env/id#synthetic-credential", "env", "id", false},
		{"wrong path", "https://app.t3.codes/pair/env/id", "env", "id", false},
		{"control", "https://app.t3.codes/threads/env/id\n", "env", "id", false},
		{"dot segment", "https://app.t3.codes/threads/env/%2e%2e", "env", "..", false},
		{"Markdown", "[任意标题](https://app.t3.codes/threads/env/id)", "env", "id", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := ""
			if tc.valid {
				want = tc.address
			}
			if got := verifiedThreadURL(tc.address, tc.environment, tc.thread); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestTaskThreadTextUsesOnlyTheOriginalReference(t *testing.T) {
	const internal = "t3-thread://v2/env/id%3Aoriginal"
	task := &Task{ThreadID: "id%3Aoriginal", Target: Target{EnvironmentID: "env"}}
	for _, link := range []string{internal, "<" + internal + ">", "[项目标题](<" + internal + ">)"} {
		task.Link = link
		if got := taskThreadText(task); got != internal || taskThreadURL(task) != "" {
			t.Fatalf("expected plain internal reference for %q, got %q", link, got)
		}
	}
	for _, link := range []string{"t3-thread://v2/env/elsewhere", "t3-thread://v2/remote/id%3Aoriginal", "t3-thread://v1/id%3Aoriginal", "https://app.t3.codes/pair?token=fake"} {
		task.Link = link
		if got := taskThreadText(task); got != "" {
			t.Fatalf("exposed unrelated reference %q", got)
		}
	}
}

func TestSendRefreshesOnlyAnExactHostedThreadLink(t *testing.T) {
	e, x, _, _ := testEngine(t)
	const hosted = "https://app.t3.codes/threads/nas-env/thread"
	x.sendLink = hosted
	accept(t, e, message("one", "NAS检查磁盘"))
	tick(t, e)
	first := onlyTask(t, e)
	if first.Link != hosted || taskThreadURL(first) != hosted {
		t.Fatalf("missing hosted link from the accepted send: %q", first.Link)
	}
	x.observation.Status = "completed"
	tick(t, e)
	x.sendLink = "https://app.t3.codes/threads/other/thread"
	next := message("two", "继续")
	next.ParentID = first.CardID
	accept(t, e, next)
	tick(t, e)
	if got := onlyTask(t, e); x.sends != 2 || got.Link != hosted {
		t.Fatalf("new turn changed the confirmed link: %q, sends %d", got.Link, x.sends)
	}
}
