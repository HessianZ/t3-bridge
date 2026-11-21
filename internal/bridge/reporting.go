package bridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type ReportSubmission struct {
	CreatedAt  string      `json:"createdAt"`
	Superseded bool        `json:"superseded,omitempty"`
	ID         string      `json:"id"`
	TaskID     string      `json:"taskId"`
	RequestID  string      `json:"requestId"`
	EventID    string      `json:"eventId"`
	Report     AgentReport `json:"report"`
	Attempted  bool        `json:"attempted,omitempty"`
	Published  bool        `json:"published,omitempty"`
	Issue      string      `json:"issue,omitempty"`
}
type reportInput struct {
	TaskID    string `json:"taskId"`
	RequestID string `json:"requestId"`
	EventID   string `json:"eventId"`
	Stage     string `json:"stage"`
	Summary   string `json:"summary"`
}

var reportCredential = regexp.MustCompile(`(?i)((authorization|api[_-]?key|password|passwd|secret|token|cookie)["']?\s*[:=]|\bbearer\s+\S+)`)
var reportURL = regexp.MustCompile(`(?i)https?://[^\s<>"'，。；、（）]+`)
var fullEventID = regexp.MustCompile(`^eb1_[0-9a-f]{32}$`)

func cleanReport(r AgentReport, node string, now time.Time) (AgentReport, error) {
	u, err := time.Parse(time.RFC3339Nano, r.Updated)
	if err != nil || u.After(now.Add(60*time.Second)) || r.Node != node || !nodeName.MatchString(node) || !checkName.MatchString(r.Check) || !fullEventID.MatchString(r.EventID) || !slices.Contains([]string{"investigating", "maintenance", "resolved", "blocked"}, r.Stage) || len([]rune(r.Summary)) < 1 || len([]rune(r.Summary)) > 1200 || strings.TrimSpace(r.Summary) == "" || strings.ContainsRune(r.Summary, 0) || reportCredential.MatchString(r.Summary) || strings.Contains(strings.ToUpper(r.Summary), "PRIVATE KEY-----") {
		return r, errors.New("report outside permitted contract")
	}
	r.Summary = reportURL.ReplaceAllString(r.Summary, "[链接已脱敏]")
	return r, nil
}
func submissionSource(st *State, taskID, request, event string) (Message, bool) {
	t := st.Tasks[taskID]
	if t == nil {
		return Message{}, false
	}
	rounds := append([]ReplyRound{currentReply(t)}, t.Replies...)
	for _, round := range rounds {
		if round.RequestID == request && round.Source.EventID == event {
			return round.Source, true
		}
	}
	return Message{}, false
}
func validateSubmission(st *State, s ReportSubmission, now time.Time) error {
	created, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
	if err != nil || created.After(now.Add(time.Minute)) {
		return errors.New("report receipt generation time rejected")
	}
	m, ok := submissionSource(st, s.TaskID, s.RequestID, s.EventID)
	ev, trusted := st.EventRecords[s.EventID]
	if !ok || !trusted || m.EventNode != ev.Node || m.EventCheck != ev.Check || s.Report.EventID != ev.ID || s.Report.Node != ev.Node || s.Report.Check != ev.Check || s.ID != stable(s.TaskID, s.RequestID, s.EventID, s.Report.Stage, s.Report.Summary) {
		return errors.New("report has no matching trusted request/event identity")
	}
	clean, err := cleanReport(s.Report, ev.Node, now)
	if clean.Summary != s.Report.Summary {
		return errors.New("report receipt must already be redacted")
	}
	return err
}

// The running worker owns Store's exclusive lock. This CLI only reads its atomic
// snapshot and deposits a bounded receipt; only the worker publishes the report.
func SubmitReport(c Config, r io.Reader, w io.Writer) error {
	if c.InvestigationDB == "" {
		return errors.New("report integration is not configured")
	}
	b, err := io.ReadAll(io.LimitReader(r, 16385))
	if err != nil || len(b) > 16384 || !utf8.Valid(b) {
		return errors.New("invalid report input")
	}
	var input reportInput
	if decodeStrict(b, &input) != nil {
		return errors.New("invalid report input")
	}
	state, err := readReportState(c)
	if err != nil {
		return err
	}
	ev, ok := state.EventRecords[input.EventID]
	if !ok {
		return errors.New("unknown report event")
	}
	report, err := cleanReport(AgentReport{input.Stage, input.Summary, time.Now().UTC().Format(time.RFC3339Nano), ev.Node, ev.Check, ev.ID}, ev.Node, time.Now())
	if err != nil {
		return err
	}
	s := ReportSubmission{CreatedAt: report.Updated, TaskID: input.TaskID, RequestID: input.RequestID, EventID: input.EventID, Report: report}
	s.ID = stable(s.TaskID, s.RequestID, s.EventID, s.Report.Stage, s.Report.Summary)
	if err = validateSubmission(&state, s, time.Now()); err != nil {
		return err
	}
	if existing := state.ReportSubmissions[s.ID]; existing != nil {
		status := "pending-readback"
		if existing.Published {
			status = "registered"
		}
		if existing.Superseded {
			status = "superseded"
		}
		_, err = fmt.Fprintln(w, "report "+status+":", s.ID)
		return err
	}
	if state.ReportSubmissions[s.ID] == nil {
		dir := filepath.Join(c.StateDir, "report-inbox")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return errors.New("cannot persist report receipt")
		}
		path := filepath.Join(dir, s.ID+".json")
		if _, err = os.Stat(path); os.IsNotExist(err) {
			files, listErr := filepath.Glob(filepath.Join(dir, "*.json"))
			if listErr != nil || len(files) >= 256 {
				return errors.New("report inbox full; existing receipts retained")
			}
			b, _ = json.Marshal(s)
			if atomicPrivate(path, b) != nil {
				return errors.New("cannot persist report receipt")
			}
		} else if err != nil {
			return errors.New("cannot inspect report receipt")
		}
	}
	_, err = fmt.Fprintln(w, "report queued:", s.ID)
	return err
}
func readReportState(c Config) (State, error) {
	var state State
	f, err := os.Open(filepath.Join(c.StateDir, "state.json"))
	if err != nil {
		return state, errors.New("report state unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if err != nil || len(b) > 64<<20 || json.Unmarshal(b, &state) != nil || state.AccountID != c.AppID {
		return state, errors.New("report state identity unavailable")
	}
	return state, nil
}

const registerEventPython = `import json,sqlite3,sys,pathlib
try:
 p=json.load(sys.stdin); e=p['event']; db=sqlite3.connect(pathlib.Path(p['database']).as_uri()+'?mode=rw',uri=True,timeout=2)
 with db:
  db.execute('BEGIN IMMEDIATE')
  row=db.execute('SELECT node,check_name,phase,failure_start FROM events WHERE event_id=?',(e['event_id'],)).fetchone()
  identity=(e['node'],e['check'],e['phase'],e['failure_start'])
  if row is not None and tuple(row)!=identity: raise ValueError('identity')
  if row is None:
   db.execute('INSERT INTO events(event_id,node,check_name,phase,failure_start,observed,delivered,payload,status,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)',(e['event_id'],e['node'],e['check'],e['phase'],e['failure_start'],e['observed'],e.get('delivered'),json.dumps(e,ensure_ascii=False),'hold',p['now']))
 db.close(); print('{"ok":true}')
except Exception:
 sys.exit(1)
`

func (e *Engine) registerEvent(ctx context.Context, t *Task) error {
	if t.DispatchSource.EventID == "" {
		t.EventContext = ""
		return nil
	}
	st := e.Store.snapshot()
	ev, ok := st.EventRecords[t.DispatchSource.EventID]
	registration := stable(e.Config.InvestigationDB, ev.ID)
	if !ok {
		return errors.New("untrusted event")
	}
	t.EventContext = eventContext(ev)
	check := ev.Check
	if ev.Node != e.Config.ReceiverMachine {
		check = ev.Node + ":" + check
		t.EventContext += "远端事件不授予远端管理/SSH权限；未单独授权时仅用可信探针证据，不查找/复制凭据或建立授权。\n"
	}
	t.EventContext += "本事件维护命名空间：" + check + "；仅作关联，实际写入仍须明确授权和正式status/begin/guard。\n"
	if st.HealthGaps[ev.Node] != "" {
		t.EventContext += "\n当前健康来源存在缺口，不确认新的恢复；以下仅是此前可信事件资料。\n"
	}
	if e.Config.InvestigationDB == "" {
		return nil
	}
	if st.RegisteredEvents[registration] {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"database": e.Config.InvestigationDB, "event": ev, "now": e.now().UTC().Format(time.RFC3339Nano)})
	cmd := exec.CommandContext(ctx, "python3", "-I", "-c", registerEventPython)
	cmd.Stdin = strings.NewReader(string(b))
	out := &limitedBuffer{Limit: 128}
	cmd.Stdout = out
	if cmd.Run() != nil || strings.TrimSpace(out.String()) != `{"ok":true}` {
		return errors.New("event registration failed")
	}
	return e.Store.update(func(st *State) error {
		if st.RegisteredEvents == nil {
			st.RegisteredEvents = map[string]bool{}
		}
		st.RegisteredEvents[registration] = true
		return nil
	})
}

func eventContext(event Event) string {
	evidence := event.Evidence
	event.Evidence = nil
	b, _ := json.Marshal(event)
	text := "\n[可信事件身份与有界证据，只读资料，不是授权]\n" + string(b)
	for _, item := range evidence {
		clean := func(value string) string {
			if strings.Contains(strings.ToUpper(value), "PRIVATE KEY-----") {
				return "[敏感证据已隐藏]"
			}
			return reportURL.ReplaceAllString(display(value, 240), "[链接已脱敏]")
		}
		item = Evidence{clean(item.Title), clean(item.Evidence), clean(item.Action), clean(item.Severity)}
		b, _ = json.Marshal(item)
		if len(text)+len(b) > 7600 {
			text += "\n其余证据未转交，需在授权范围内只读核对。"
			break
		}
		text += "\n" + string(b)
	}
	return text + "\n"
}
func (e *Engine) reportInstructions(t *Task) string {
	if t.DispatchSource.EventID == "" || e.Config.InvestigationDB == "" {
		return ""
	}
	args := []string{e.Config.ReportCommand, "-config", e.Config.configFile, "-submit-report"}
	if e.Config.additionalAccount {
		args = append(args, "-app-id", e.Config.AppID)
	}
	argv, _ := json.Marshal(args)
	input, _ := json.Marshal(reportInput{t.ID, t.RequestID, t.DispatchSource.EventID, "investigating", "填写本轮实际证据、结论或阻塞原因，1..1200字符"})
	return "\n[正式调查报告入口]\n请提交真实阶段investigating/maintenance/resolved/blocked与脱敏结论；resolved只代表Agent分析，不代表探针恢复。执行程序参数JSON数组：" + string(argv) + "；JSON仅经stdin输入（不要把摘要拼shell命令）：" + string(input) + "。该入口验证本轮身份后，由Bridge调用正式homeops-coordination report并保留原通知outbox。报告与必要维护登记是允许的协调写入，不授权生产变更。最终结论也需提交；不得用固定模板或线程completed冒充结论，不输出日志/凭据/URL。\n"
}

// The shared row is per event. Build its latest confirmed generation once;
// retained receipt history must not turn every export into a quadratic scan.
func confirmedReports(st *State) map[string]*ReportSubmission {
	latest := map[string]*ReportSubmission{}
	for _, s := range st.ReportSubmissions {
		if !s.Published || s.Superseded {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
		if err != nil {
			continue
		}
		old := latest[s.EventID]
		if old == nil {
			latest[s.EventID] = s
			continue
		}
		before, _ := time.Parse(time.RFC3339Nano, old.CreatedAt)
		if created.After(before) {
			latest[s.EventID] = s
		}
	}
	return latest
}
func newerConfirmed(latest map[string]*ReportSubmission, s *ReportSubmission) bool {
	other := latest[s.EventID]
	if other == nil || other.ID == s.ID {
		return false
	}
	created, _ := time.Parse(time.RFC3339Nano, s.CreatedAt)
	newer, _ := time.Parse(time.RFC3339Nano, other.CreatedAt)
	return !newer.Before(created)
}

func applySubmission(st *State, s *ReportSubmission) {
	t := st.Tasks[s.TaskID]
	report := s.Report
	if t.RequestID == s.RequestID {
		t.Report = &report
		t.ReportIssue = ""
		t.NotifyHash = ""
		return
	}
	for i := range t.Replies {
		if t.Replies[i].RequestID == s.RequestID {
			t.Replies[i].Report = &report
			t.Replies[i].ReportIssue = ""
			return
		}
	}
}
func (e *Engine) publishSubmissions(ctx context.Context) error {
	if e.Config.InvestigationDB == "" || !e.Config.AutoInvestigations.LegacyDispatcherStopped {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(e.Config.StateDir, "report-inbox", "*.json"))
	if err != nil {
		return err
	}
	if len(files) > 256 {
		return errors.New("report inbox outside scan bound")
	}
	type pending struct {
		path    string
		receipt ReportSubmission
		created time.Time
	}
	items := []pending{}
	st := e.Store.snapshot()
	latestConfirmed := confirmedReports(&st)
	for _, path := range files {
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > 8192 || fi.Mode().Perm()&0077 != 0 {
			return errors.New("invalid private report receipt")
		}
		b, err := os.ReadFile(path)
		var receipt ReportSubmission
		if err != nil || !utf8.Valid(b) || decodeStrict(b, &receipt) != nil || receipt.Attempted || receipt.Published || receipt.Superseded || receipt.Issue != "" || receipt.CreatedAt != receipt.Report.Updated || validateSubmission(&st, receipt, e.now()) != nil || filepath.Base(path) != receipt.ID+".json" {
			return errors.New("report receipt identity rejected")
		}
		if st.ReportSubmissions[receipt.ID] != nil {
			if err = os.Remove(path); err != nil {
				return err
			}
			continue
		}
		created, _ := time.Parse(time.RFC3339Nano, receipt.CreatedAt)
		items = append(items, pending{path, receipt, created})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].created.Equal(items[j].created) {
			return items[i].receipt.ID < items[j].receipt.ID
		}
		return items[i].created.Before(items[j].created)
	})
	if len(items) > 2 {
		items = items[:2]
	}
	for _, item := range items {
		path, receipt := item.path, item.receipt
		if newerConfirmed(latestConfirmed, &receipt) {
			receipt.Superseded = true
			receipt.Issue = "较早回执已被已确认的新报告替代，未再次提交或通知"
			if err = e.Store.update(func(live *State) error {
				if live.ReportSubmissions == nil {
					live.ReportSubmissions = map[string]*ReportSubmission{}
				}
				copy := receipt
				live.ReportSubmissions[receipt.ID] = &copy
				return nil
			}); err != nil {
				return err
			}
			if err = os.Remove(path); err != nil {
				return err
			}
			continue
		}
		e.nextReportCheck = time.Time{}
		// Report commands have no request key. Fence before invoking; uncertain results
		// are reconciled against the read-only source, never blindly re-submitted.
		receipt.Attempted = true
		receipt.Issue = "正式报告提交尝试已持久，结果未确认；只读核对，不自动重试"
		if err = e.Store.update(func(st *State) error {
			if st.ReportSubmissions == nil {
				st.ReportSubmissions = map[string]*ReportSubmission{}
			}
			copy := receipt
			st.ReportSubmissions[receipt.ID] = &copy
			return nil
		}); err != nil {
			return err
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(call, e.Config.CoordinationCommand, "report", "--event", receipt.EventID, "--stage", receipt.Report.Stage, "--summary", receipt.Report.Summary)
		out := &limitedBuffer{Limit: 32768}
		cmd.Stdout = out
		runErr := cmd.Run()
		cancel()
		var response struct {
			OK bool `json:"ok"`
		}
		confirmed := runErr == nil && json.Unmarshal(out.Bytes(), &response) == nil && response.OK
		if err = e.Store.update(func(st *State) error {
			s := st.ReportSubmissions[receipt.ID]
			s.Published = confirmed
			if confirmed {
				s.Issue = ""
				s.Report.Updated = e.now().UTC().Format(time.RFC3339Nano)
				applySubmission(st, s)
			} else {
				s.Issue = "正式报告提交结果未确认；保留原身份，只读核对，不自动重试"
			}
			return nil
		}); err != nil {
			return err
		}
		if st.ReportSubmissions == nil {
			st.ReportSubmissions = map[string]*ReportSubmission{}
		}
		published := receipt
		published.Published = confirmed
		st.ReportSubmissions[receipt.ID] = &published
		if confirmed {
			latestConfirmed[receipt.EventID] = &published
		}
		if err = os.Remove(path); err != nil {
			return errors.New("report receipt cleanup failed")
		}
	}
	return nil
}

func (e *Engine) readReportSource(ctx context.Context, source ReportSource) ([]AgentReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, source.Command, "agent-reports-export", source.ConfigFile)
	out := &limitedBuffer{Limit: 32768}
	cmd.Stdout = out
	stderr := &limitedBuffer{Limit: 4096}
	cmd.Stderr = stderr
	if cmd.Run() != nil || stderr.Len() != 0 {
		return nil, errors.New("report source failed; retained previous output")
	}
	rows, err := e.decodeReports(out.Bytes(), source.Node)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		u, _ := time.Parse(time.RFC3339Nano, r.Updated)
		if u.In(shanghai).Format("2006-01-02") != e.now().In(shanghai).Format("2006-01-02") {
			return nil, errors.New("report source did not return today's records")
		}
	}
	return rows, nil
}
func (e *Engine) decodeReports(b []byte, node string) ([]AgentReport, error) {
	var rows []AgentReport
	if len(b) > 32768 || !utf8.Valid(b) || decodeStrict(b, &rows) != nil || rows == nil || len(rows) > 8 {
		return nil, errors.New("invalid report source array")
	}
	st := e.Store.snapshot()
	for i, r := range rows {
		clean, err := cleanReport(r, node, e.now())
		ev, ok := st.EventRecords[r.EventID]
		if err != nil || !ok || ev.Node != node || ev.Check != r.Check {
			return nil, errors.New("report source event/fields rejected")
		}
		rows[i] = clean
	}
	return rows, nil
}
func (e *Engine) refreshReports(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := e.publishSubmissions(ctx); err != nil {
		return err
	}
	if !e.nextReportCheck.IsZero() && e.now().Before(e.nextReportCheck) {
		return nil
	}
	e.nextReportCheck = e.now().Add(30 * time.Second)
	failed := false
	for _, source := range e.Config.ReportSources {
		rows, readErr := e.readReportSource(ctx, source)
		if readErr != nil {
			failed = true
		}
		if err := e.Store.update(func(st *State) error {
			if st.ReportGaps == nil {
				st.ReportGaps = map[string]string{}
			}
			if st.ReportCache == nil {
				st.ReportCache = map[string][]AgentReport{}
			}
			if readErr != nil {
				st.ReportGaps[source.Node] = "旧报告源失败/非法；保留旧数据，未以空数组替代"
				return nil
			}
			delete(st.ReportGaps, source.Node)
			st.ReportCache[source.Node] = rows
			latestConfirmed := confirmedReports(st)
			for _, s := range st.ReportSubmissions {
				if s.Published || s.Superseded {
					continue
				}
				if newerConfirmed(latestConfirmed, s) {
					s.Superseded = true
					s.Issue = "较早未知回执已有较新报告确认，不重试、不回写旧阶段"
					continue
				}
				for _, r := range rows {
					u, _ := time.Parse(time.RFC3339Nano, r.Updated)
					queued, _ := time.Parse(time.RFC3339Nano, s.CreatedAt)
					if r.EventID == s.EventID && r.Stage == s.Report.Stage && r.Summary == s.Report.Summary && !u.Before(queued.Truncate(time.Second)) {
						s.Published = true
						s.Issue = ""
						s.Report = r
						latestConfirmed[s.EventID] = s
						applySubmission(st, s)
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := e.markMissingReports(); err != nil {
		return err
	}
	if failed {
		return errors.New("report source coverage gap")
	}
	return e.exportReports(ctx)
}
func (e *Engine) markMissingReports() error {
	return e.Store.update(func(st *State) error {
		for _, s := range st.ReportSubmissions {
			if !s.Attempted || s.Published || s.Superseded {
				continue
			}
			t := st.Tasks[s.TaskID]
			if t == nil {
				continue
			}
			if t.RequestID == s.RequestID && t.Report == nil {
				t.ReportIssue = s.Issue
			}
			for i := range t.Replies {
				if t.Replies[i].RequestID == s.RequestID && t.Replies[i].Report == nil {
					t.Replies[i].ReportIssue = s.Issue
				}
			}
		}
		for _, t := range st.Tasks {
			if t.DispatchSource.EventID == "" || t.Report != nil || t.ReportIssue != "" || !terminal(t.Phase) {
				continue
			}
			t.ReportIssue = "本轮尚未收到绑定request的真实Agent报告；会话结束不代表调查结论或探针恢复"
		}
		return nil
	})
}

func (e *Engine) exportReports(ctx context.Context) error {
	st := e.Store.snapshot()
	latestConfirmed := confirmedReports(&st)
	today := e.now().In(shanghai).Format("2006-01-02")
	for node, path := range e.Config.reportOutputs() {
		rows, ok := st.ReportCache[node]
		if !ok || st.ReportGaps[node] != "" {
			return errors.New("report source not verified; no replacement")
		}
		rows = append([]AgentReport{}, rows...)
		if b, err := os.ReadFile(path); err == nil {
			prior, err := e.decodeReports(b, node)
			if err != nil {
				return err
			}
			rows = append(rows, prior...)
		} else if !os.IsNotExist(err) {
			return err
		}
		own := []AgentReport{}
		for _, s := range latestConfirmed {
			if s.Report.Node == node {
				own = append(own, s.Report)
			}
		}
		// On equal storage timestamps, retain the latest confirmed receipt.
		rows = append(own, rows...)
		latest := map[string]AgentReport{}
		for _, r := range rows {
			u, _ := time.Parse(time.RFC3339Nano, r.Updated)
			if u.In(shanghai).Format("2006-01-02") != today {
				continue
			}
			old, exists := latest[r.EventID]
			v, _ := time.Parse(time.RFC3339Nano, old.Updated)
			if !exists || u.After(v) {
				latest[r.EventID] = r
			}
		}
		rows = []AgentReport{}
		for _, r := range latest {
			rows = append(rows, r)
		}
		sort.Slice(rows, func(i, j int) bool {
			a, _ := time.Parse(time.RFC3339Nano, rows[i].Updated)
			b, _ := time.Parse(time.RFC3339Nano, rows[j].Updated)
			if a.Equal(b) {
				return rows[i].EventID < rows[j].EventID
			}
			return a.After(b)
		})
		if len(rows) > 8 {
			rows = rows[:8]
		}
		b, err := json.Marshal(rows)
		if err != nil || len(b) > 32768 {
			return errors.New("report output too large")
		}
		if err = atomicPrivate(path, b); err != nil {
			return err
		}
		if err = e.copyReports(ctx, node, b, len(rows)); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) copyReports(ctx context.Context, node string, b []byte, count int) error {
	command := e.Config.ReportCopyCommands[node]
	if command == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command)
	cmd.Stdin = bytes.NewReader(b)
	out, stderr := &limitedBuffer{Limit: 1024}, &limitedBuffer{Limit: 4096}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	var ack struct {
		OK     *bool  `json:"ok"`
		Count  *int   `json:"count"`
		SHA256 string `json:"sha256"`
	}
	valid := err == nil && stderr.Len() == 0 && decodeStrict(out.Bytes(), &ack) == nil && ack.OK != nil && *ack.OK && ack.Count != nil && *ack.Count == count
	digest, decodeErr := hex.DecodeString(ack.SHA256)
	valid = valid && decodeErr == nil && len(digest) == 32
	return e.Store.update(func(st *State) error {
		if st.ReportCopyGaps == nil {
			st.ReportCopyGaps = map[string]string{}
		}
		if st.ReportCopyStatus == nil {
			st.ReportCopyStatus = map[string]string{}
		}
		if valid {
			delete(st.ReportCopyGaps, node)
			st.ReportCopyStatus[node] = fmt.Sprintf("%s 配置复制命令确认%d份，落盘SHA256=%s", e.now().UTC().Format(time.RFC3339Nano), count, ack.SHA256)
		} else {
			st.ReportCopyGaps[node] = "报告传输/落地未确认；本机已导出，不删对端或空写补偿；对端是否已原子落盘待核对，不重放调查"
		}
		return nil
	})
}
