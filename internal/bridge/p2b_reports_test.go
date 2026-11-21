package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type reportFixture struct{ db, sourceMode, reportMode, count string }

func reportSQL(t *testing.T, db string, commands ...[]any) []byte {
	t.Helper()
	b, _ := json.Marshal(commands)
	cmd := exec.Command("python3", "-I", "-c", `import sqlite3,json,sys
c=sqlite3.connect(sys.argv[1]); out=[]
for sql,args in json.load(sys.stdin): out.append(c.execute(sql,args).fetchall())
c.commit();print(json.dumps(out,ensure_ascii=False));c.close()`, db)
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic SQLite fixture: %v %s", err, out)
	}
	return out
}
func setupReports(t *testing.T, e *Engine) reportFixture {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python stdlib SQLite required")
	}
	dir := t.TempDir()
	f := reportFixture{filepath.Join(dir, "shared state?雪.sqlite"), filepath.Join(dir, "source-mode"), filepath.Join(dir, "report-mode"), filepath.Join(dir, "calls")}
	reportSQL(t, f.db, []any{`CREATE TABLE events(event_id TEXT PRIMARY KEY,node TEXT NOT NULL,check_name TEXT NOT NULL,phase TEXT NOT NULL,failure_start TEXT NOT NULL,observed TEXT NOT NULL,delivered TEXT,payload TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,last_attempt_at TEXT,uncertain_since TEXT,accepted_at TEXT,delivery_id TEXT,created_at TEXT NOT NULL)`, []any{}}, []any{`CREATE TABLE analysis_reports(event_id TEXT PRIMARY KEY,stage TEXT NOT NULL,summary TEXT NOT NULL,updated TEXT NOT NULL)`, []any{}}, []any{`CREATE TABLE followups(id TEXT PRIMARY KEY,text TEXT NOT NULL,sent_at TEXT,attempts INTEGER NOT NULL DEFAULT 0)`, []any{}}, []any{`CREATE TABLE operations(check_name TEXT PRIMARY KEY,owner TEXT,reason TEXT,started TEXT,until_at TEXT)`, []any{}}, []any{`INSERT INTO operations VALUES ('service:gitea','another-owner','synthetic maintenance','start','expired')`, []any{}})
	os.Chmod(f.db, 0600)
	constants, _ := json.Marshal(map[string]string{"db": f.db, "sourceMode": f.sourceMode, "reportMode": f.reportMode, "count": f.count})
	header := "#!/usr/bin/env python3\nimport json,sqlite3,sys,pathlib,datetime,hashlib\np=json.loads(" + fmt.Sprintf("%q", string(constants)) + ")\n"
	source := header + `mode=pathlib.Path(p['sourceMode']).read_text() if pathlib.Path(p['sourceMode']).exists() else ''
if mode=='fail': sys.exit(1)
if mode=='null': print('null'); sys.exit(0)
if mode=='oversized': print('x'*32769); sys.exit(0)
if mode=='empty': print('[]'); sys.exit(0)
assert sys.argv[1]=='agent-reports-export'
cfg=json.loads(pathlib.Path(sys.argv[2]).read_text()); c=sqlite3.connect(pathlib.Path(cfg['DatabasePath']).as_uri()+'?mode=ro',uri=True)
rows=c.execute('SELECT a.stage,a.summary,a.updated,e.node,e.check_name,e.event_id FROM analysis_reports a JOIN events e ON a.event_id=e.event_id WHERE e.node=? ORDER BY a.updated DESC,e.event_id LIMIT 8',(cfg['Node'],)).fetchall()
print(json.dumps([dict(zip(('Stage','Summary','Updated','Node','Check','EventID'),r)) for r in rows],ensure_ascii=False))
`
	sourcePath := filepath.Join(dir, "source")
	os.WriteFile(sourcePath, []byte(source), 0700)
	coord := header + `if sys.argv[1]=='status': print('correctly bounded by the engine fake'); sys.exit(0)
assert sys.argv[1]=='report'
args=dict(zip(sys.argv[2::2],sys.argv[3::2])); c=sqlite3.connect(p['db']); eid=args['--event']; stage=args['--stage']; summary=args['--summary']
assert c.execute('SELECT event_id FROM events WHERE event_id=?',(eid,)).fetchone()
now=datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'); key='report:'+eid+':'+hashlib.sha256((stage+summary).encode()).hexdigest()
with c:
 c.execute('INSERT INTO analysis_reports VALUES (?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET stage=excluded.stage,summary=excluded.summary,updated=excluded.updated',(eid,stage,summary,now))
 c.execute('INSERT OR IGNORE INTO followups(id,text) VALUES (?,?)',(key,summary))
count=pathlib.Path(p['count']);count.write_text(str(int(count.read_text() if count.exists() else '0')+1))
if pathlib.Path(p['reportMode']).exists(): sys.exit(1)
print(''+json.dumps({'ok':True,'operations':[]}))
`
	coordPath := filepath.Join(dir, "coordination")
	os.WriteFile(coordPath, []byte(coord), 0700)
	configPath := filepath.Join(dir, "export.json")
	b, _ := json.Marshal(map[string]string{"Node": "example-nas", "DatabasePath": f.db})
	os.WriteFile(configPath, b, 0600)
	e.Config.ReportSources = []ReportSource{{"example-nas", sourcePath, configPath}}
	e.Config.InvestigationDB = f.db
	e.Config.AutoInvestigations.LegacyDispatcherStopped = true
	e.Config.ReportCommand = "/synthetic/t3-bridge"
	e.Config.CoordinationCommand = coordPath
	e.Config.configFile = filepath.Join(dir, "bridge-config.json")
	e.Config.ReportOutputs = map[string]string{"example-nas": filepath.Join(dir, "reports.json")}
	e.Config.EventChats = []string{"chat"}
	e.Config.EventRoutes = map[string]string{"example-nas": "nas"}
	if err := e.Store.BindAccount(e.Config.AppID); err != nil {
		t.Fatal(err)
	}
	return f
}
func seedOldReports(t *testing.T, e *Engine, f reportFixture, n int) []AgentReport {
	t.Helper()
	rows := []AgentReport{}
	commands := [][]any{}
	for i := 0; i < n; i++ {
		ev := syntheticEvent("example-nas", fmt.Sprintf("check:%02d", i), "fault", e.now().Add(-time.Duration(i+1)*time.Minute))
		b, _ := json.Marshal(ev)
		r := AgentReport{"resolved", strings.Repeat("旧", 1100) + fmt.Sprint(i), e.now().Add(-time.Duration(i+1) * time.Second).UTC().Format(time.RFC3339Nano), ev.Node, ev.Check, ev.ID}
		rows = append(rows, r)
		commands = append(commands, []any{`INSERT INTO events(event_id,node,check_name,phase,failure_start,observed,payload,status,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, []any{ev.ID, ev.Node, ev.Check, ev.Phase, ev.FailureStart, ev.Observed, string(b), "accepted", r.Updated}}, []any{`INSERT INTO analysis_reports VALUES (?,?,?,?)`, []any{r.EventID, r.Stage, r.Summary, r.Updated}})
		if err := e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	reportSQL(t, f.db, commands...)
	return rows
}
func queueReport(t *testing.T, e *Engine, task *Task, stage, summary string) string {
	t.Helper()
	b, _ := json.Marshal(reportInput{task.ID, task.RequestID, task.DispatchSource.EventID, stage, summary})
	var out bytes.Buffer
	if err := SubmitReport(e.Config, bytes.NewReader(b), &out); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.TrimPrefix(out.String(), "report queued:"))
}
func TestP2bReportSourceRetainsOldFullConclusionsAndFailsClosed(t *testing.T) {
	e, _, _, _ := testEngine(t)
	f := setupReports(t, e)
	old := seedOldReports(t, e, f, 18)
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := e.Config.ReportOutputs["example-nas"]
	before, _ := os.ReadFile(path)
	var rows []AgentReport
	if json.Unmarshal(before, &rows) != nil || len(rows) != 8 || rows[0].Summary != old[0].Summary || len([]rune(rows[0].Summary)) < 1000 {
		t.Fatal("old real conclusions were truncated/replaced")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0600 {
		t.Fatal("report output is not private")
	}
	for _, mode := range []string{"fail", "null", "oversized"} {
		os.WriteFile(f.sourceMode, []byte(mode), 0600)
		e.nextReportCheck = time.Time{}
		if e.refreshReports(context.Background()) == nil {
			t.Fatal("bad source accepted", mode)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("source failure replaced previous report output")
		}
	}
	os.WriteFile(f.sourceMode, []byte("empty"), 0600)
	e.nextReportCheck = time.Time{}
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("empty new data erased valid retained reports")
	}
	if len(reportSQL(t, f.db, []any{`SELECT * FROM operations`, []any{}})) == 0 {
		t.Fatal("maintenance state missing")
	}
}
func TestP2bSubmittedReportBindsExactRoundAndFormalOutbox(t *testing.T) {
	e, x, l, _ := testEngine(t)
	f := setupReports(t, e)
	seedOldReports(t, e, f, 8)
	accept(t, e, message("plain", "/nas 普通咨询"))
	tick(t, e)
	original := onlyTask(t, e).Source
	x.observation.Status = "completed"
	tick(t, e)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	l.message.Quote = "事件：" + ev.ID
	m := message("event", "仅解释原因，不实施变更")
	m.ParentID = "alert"
	m.RootID = original.ID
	accept(t, e, m)
	tick(t, e)
	investigation := onlyTask(t, e)
	if !strings.Contains(x.request, "-submit-report") || !strings.Contains(x.request, investigation.RequestID) {
		t.Fatal("Agent has no bound report contract")
	}
	beforeOps := reportSQL(t, f.db, []any{`SELECT * FROM operations`, []any{}})
	id := queueReport(t, e, investigation, "resolved", "实际只读结论：没有发现磁盘错误。参考 https://example.invalid/private。")
	queueReport(t, e, investigation, "resolved", "实际只读结论：没有发现磁盘错误。参考 https://example.invalid/private。")
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := onlyTask(t, e)
	if task.Report == nil || task.Report.Stage != "resolved" || !strings.Contains(task.Report.Summary, "[链接已脱敏]") || !reflect.DeepEqual(task.Source, original) || !e.Store.snapshot().ReportSubmissions[id].Published {
		t.Fatal("report did not retain exact round/source identity")
	}
	if got, _ := os.ReadFile(f.count); string(got) != "1" {
		t.Fatal("repeated receipt repeated formal report command")
	}
	if !bytes.Equal(beforeOps, reportSQL(t, f.db, []any{`SELECT * FROM operations`, []any{}})) {
		t.Fatal("report/registration changed another owner's maintenance")
	}
	var rows [][][]any
	json.Unmarshal(reportSQL(t, f.db, []any{`SELECT status FROM events WHERE event_id=?`, []any{ev.ID}}, []any{`SELECT count(*) FROM followups`, []any{}}), &rows)
	if rows[0][0][0] != "hold" || rows[1][0][0] != float64(1) {
		t.Fatal("new event re-enabled legacy dispatcher or omitted formal outbox")
	}
	l.message.Quote = ""
	x.observation.Status = "completed"
	tick(t, e)
	accept(t, e, message("next", "/nas 无关的新咨询"))
	tick(t, e)
	latest := onlyTask(t, e)
	if latest.Report != nil || latest.DispatchSource.EventID != ev.ID || latest.Replies[len(latest.Replies)-1].Report == nil || latest.CardID == task.CardID || latest.RequestID == task.RequestID {
		t.Fatal("implicit event continuation lost per-round report/card identity")
	}
	if !strings.Contains(x.request, "仅获只读调查授权") || !strings.Contains(x.request, latest.RequestID) {
		t.Fatal("implicit continuation reused old report arguments")
	}
}
func TestP2bUnknownReportPublicationReconcilesWithoutRetryAfterRestart(t *testing.T) {
	e, _, l, _ := testEngine(t)
	f := setupReports(t, e)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	l.message.Quote = "事件：" + ev.ID
	m := message("one", "请解释")
	m.ParentID = "alert"
	accept(t, e, m)
	tick(t, e)
	task := onlyTask(t, e)
	os.WriteFile(f.reportMode, []byte("lost"), 0600)
	os.WriteFile(f.sourceMode, []byte("fail"), 0600)
	id := queueReport(t, e, task, "blocked", "只读取证遇到权限边界，没有实施修改。")
	if e.refreshReports(context.Background()) == nil {
		t.Fatal("source failure hidden")
	}
	if e.Store.snapshot().ReportSubmissions[id].Published {
		t.Fatal("unknown publication falsely confirmed")
	}
	e.Store.Close()
	s, err := OpenStore(e.Config.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	e.nextReportCheck = time.Time{}
	os.Remove(f.sourceMode)
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.snapshot().ReportSubmissions[id].Published || s.snapshot().Tasks[task.ID].Report == nil {
		t.Fatal("formal read-only result failed to reconcile original report")
	}
	if got, _ := os.ReadFile(f.count); string(got) != "1" {
		t.Fatal("restart retried uncertain formal report command")
	}
}
func TestP2bMissingAgentReportCannotBeInventedFromCompleted(t *testing.T) {
	e, x, l, _ := testEngine(t)
	f := setupReports(t, e)
	seedOldReports(t, e, f, 8)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	l.message.Quote = "事件：" + ev.ID
	m := message("one", "只解释")
	m.ParentID = "alert"
	accept(t, e, m)
	tick(t, e)
	x.observation.Status = "completed"
	x.observation.Summary = "模型最终正文，不是结构化报告"
	tick(t, e)
	e.nextReportCheck = time.Time{}
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := onlyTask(t, e)
	if task.Report != nil || !strings.Contains(task.ReportIssue, "尚未收到") || !strings.Contains(card(task), "不代表调查结论或探针恢复") {
		t.Fatal("completed fabricated an Agent report or hid missing evidence")
	}
	b, _ := os.ReadFile(e.Config.ReportOutputs["example-nas"])
	if bytes.Contains(b, []byte(ev.ID)) {
		t.Fatal("fixed stage template exported as a real conclusion")
	}
}

func TestP2bReportArrayFieldsAndNodeAreValidated(t *testing.T) {
	e, _, _, _ := testEngine(t)
	ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	valid := AgentReport{"investigating", "实际证据", e.now().UTC().Format(time.RFC3339Nano), ev.Node, ev.Check, ev.ID}
	for _, mode := range []string{"node", "check", "event", "stage", "future", "time", "long", "null", "nine", "unknown-field", "credential", "utf8"} {
		r := valid
		switch mode {
		case "node":
			r.Node = "example-router"
		case "check":
			r.Check = "wrong"
		case "event":
			r.EventID = "eb1_" + strings.Repeat("f", 32)
		case "stage":
			r.Stage = "recovered"
		case "future":
			r.Updated = e.now().Add(61 * time.Second).Format(time.RFC3339Nano)
		case "time":
			r.Updated = "bad"
		case "long":
			r.Summary = strings.Repeat("界", 1201)
		case "credential":
			r.Summary = `{"api_key":"synthetic"}`
		}
		rows := []AgentReport{r}
		if mode == "nine" {
			for len(rows) < 9 {
				rows = append(rows, r)
			}
		}
		b, _ := json.Marshal(rows)
		if mode == "null" {
			b = []byte("null")
		}
		if mode == "unknown-field" {
			b = []byte(`[{"unexpected":"value"}]`)
		}
		if mode == "utf8" {
			b = []byte{'[', 0xff, ']'}
		}
		if _, err := e.decodeReports(b, "example-nas"); err == nil {
			t.Fatal("invalid source report accepted", mode)
		}
	}
	valid.Summary = "证据 HTTP://example.invalid/path，保留结论。"
	b, _ := json.Marshal([]AgentReport{valid})
	rows, err := e.decodeReports(b, "example-nas")
	if err != nil || rows[0].Summary != "证据 [链接已脱敏]，保留结论。" {
		t.Fatal("URL redaction changed the actual conclusion", err)
	}
}

func setReceiptTime(t *testing.T, e *Engine, id string, at time.Time) {
	t.Helper()
	path := filepath.Join(e.Config.StateDir, "report-inbox", id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt ReportSubmission
	if json.Unmarshal(b, &receipt) != nil {
		t.Fatal("receipt fixture")
	}
	receipt.CreatedAt = at.UTC().Format(time.RFC3339Nano)
	receipt.Report.Updated = receipt.CreatedAt
	b, _ = json.Marshal(receipt)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestP2bReportReceiptsOrderByCreationAndNeverRegressAfterRestart(t *testing.T) {
	for _, backlog := range []bool{false, true} {
		t.Run(map[bool]string{false: "hash-reversed-two-stages", true: "more-than-two-and-restart"}[backlog], func(t *testing.T) {
			e, _, l, _ := testEngine(t)
			f := setupReports(t, e)
			ev := syntheticEvent("example-nas", "hardware:smart", "fault", e.now().Add(-time.Minute))
			e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
			l.message.Quote = "事件：" + ev.ID
			m := message("one", "只读分析")
			m.ParentID = "alert"
			accept(t, e, m)
			tick(t, e)
			task := onlyTask(t, e)
			firstSummary := "最初实际分析证据"
			firstID := stable(task.ID, task.RequestID, ev.ID, "investigating", firstSummary)
			resolvedSummary, resolvedID := "", ""
			for i := 0; i < 1000; i++ {
				text := fmt.Sprintf("最新实际结论%d", i)
				id := stable(task.ID, task.RequestID, ev.ID, "resolved", text)
				if id < firstID {
					resolvedSummary, resolvedID = text, id
					break
				}
			}
			if resolvedID == "" {
				t.Fatal("cannot construct reverse hash fixture")
			}
			base := time.Now().Add(-10 * time.Second)
			ids := []string{}
			id := queueReport(t, e, task, "investigating", firstSummary)
			setReceiptTime(t, e, id, base)
			ids = append(ids, id)
			if backlog {
				for i, stage := range []string{"investigating", "maintenance"} {
					id = queueReport(t, e, task, stage, fmt.Sprintf("实际中间证据%d", i))
					setReceiptTime(t, e, id, base.Add(time.Duration(i+1)*time.Second))
					ids = append(ids, id)
				}
			}
			id = queueReport(t, e, task, "resolved", resolvedSummary)
			setReceiptTime(t, e, id, base.Add(time.Duration(len(ids))*time.Second))
			ids = append(ids, id)
			if id != resolvedID || !(resolvedID < firstID) {
				t.Fatal("resolved receipt was not first in filename order")
			}
			if err := e.refreshReports(context.Background()); err != nil {
				t.Fatal(err)
			}
			if backlog {
				if files, _ := filepath.Glob(filepath.Join(e.Config.StateDir, "report-inbox", "*.json")); len(files) != 2 {
					t.Fatal("batch exceeded two validated receipts")
				}
				e.Store.Close()
				s, err := OpenStore(e.Config.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				e.Store = s
				if err := e.refreshReports(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if task = onlyTask(t, e); task.Report == nil || task.Report.Stage != "resolved" || task.Report.Summary != resolvedSummary {
				t.Fatal("card regressed to an older generated stage")
			}
			var out []AgentReport
			b, _ := os.ReadFile(e.Config.ReportOutputs["example-nas"])
			if json.Unmarshal(b, &out) != nil || len(out) != 1 || out[0].Stage != "resolved" || out[0].Summary != resolvedSummary {
				t.Fatal("export regressed to an older stage")
			}
			var sql [][][]any
			json.Unmarshal(reportSQL(t, f.db, []any{`SELECT stage,summary FROM analysis_reports WHERE event_id=?`, []any{ev.ID}}, []any{`SELECT text FROM followups ORDER BY rowid`, []any{}}), &sql)
			if sql[0][0][0] != "resolved" || sql[1][0][0] != firstSummary || sql[1][len(ids)-1][0] != resolvedSummary {
				t.Fatal("SQLite/outbox notification order followed hashes instead of generation time")
			}
			late := queueReport(t, e, task, "investigating", "晚扫描的旧证据")
			setReceiptTime(t, e, late, base.Add(-time.Second))
			e.Store.Close()
			s, err := OpenStore(e.Config.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e.Store = s
			if err := e.refreshReports(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !s.snapshot().ReportSubmissions[late].Superseded || onlyTask(t, e).Report.Stage != "resolved" {
				t.Fatal("late old receipt overwrote confirmed current stage")
			}
			calls, _ := os.ReadFile(f.count)
			if string(calls) != fmt.Sprint(len(ids)) {
				t.Fatal("late old receipt was re-submitted or notified")
			}
			json.Unmarshal(reportSQL(t, f.db, []any{`SELECT stage FROM analysis_reports WHERE event_id=?`, []any{ev.ID}}), &sql)
			if sql[0][0][0] != "resolved" {
				t.Fatal("late scan regressed SQLite")
			}
		})
	}
}

func TestP2bOptionalFixedReportCopyPreservesLocalAndRemoteOnFailure(t *testing.T) {
	e, x, _, _ := testEngine(t)
	f := setupReports(t, e)
	var source map[string]string
	b, _ := os.ReadFile(e.Config.ReportSources[0].ConfigFile)
	json.Unmarshal(b, &source)
	source["Node"] = "example-router"
	b, _ = json.Marshal(source)
	os.WriteFile(e.Config.ReportSources[0].ConfigFile, b, 0600)
	e.Config.ReportSources[0].Node = "example-router"
	local := filepath.Join(t.TempDir(), "example-router.json")
	e.Config.ReportOutputs = map[string]string{"example-router": local}
	ev := syntheticEvent("example-router", "hardware:smart", "fault", e.now().Add(-time.Minute))
	e.Store.update(func(st *State) error { st.EventRecords[ev.ID] = ev; return nil })
	payload, _ := json.Marshal(ev)
	r := AgentReport{"blocked", "实际路由器只读结论", e.now().Format(time.RFC3339Nano), ev.Node, ev.Check, ev.ID}
	reportSQL(t, f.db, []any{`INSERT INTO events(event_id,node,check_name,phase,failure_start,observed,payload,status,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, []any{ev.ID, ev.Node, ev.Check, ev.Phase, ev.FailureStart, ev.Observed, string(payload), "hold", r.Updated}}, []any{`INSERT INTO analysis_reports VALUES (?,?,?,?)`, []any{ev.ID, r.Stage, r.Summary, r.Updated}})
	dir := t.TempDir()
	peer := filepath.Join(dir, "peer.json")
	mode := filepath.Join(dir, "mode")
	script := filepath.Join(dir, "fixed-copy")
	program := fmt.Sprintf("#!/usr/bin/env python3\nimport json,sys,pathlib,hashlib\np=pathlib.Path(%q);mode=pathlib.Path(%q)\n", peer, mode) + `rows=json.load(sys.stdin)
if mode.exists(): sys.exit(1)
if not rows and p.exists() and json.loads(p.read_text()): sys.exit(1)
b=json.dumps(rows,ensure_ascii=False).encode();p.write_bytes(b)
print(json.dumps({'ok':True,'count':len(rows),'sha256':hashlib.sha256(b).hexdigest()}))
`
	os.WriteFile(script, []byte(program), 0700)
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(peer); !os.IsNotExist(err) {
		t.Fatal("default-off copy hook ran")
	}
	e.Config.ReportCopyCommands = map[string]string{"example-router": script}
	e.nextReportCheck = time.Time{}
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	prior, _ := os.ReadFile(peer)
	if len(prior) == 0 || e.Store.snapshot().ReportCopyStatus["example-router"] == "" {
		t.Fatal("configured command acknowledgement missing")
	}
	os.WriteFile(mode, []byte("fail"), 0600)
	e.nextReportCheck = time.Time{}
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(peer)
	localData, _ := os.ReadFile(local)
	if !bytes.Equal(prior, after) || len(localData) == 0 || e.Store.snapshot().ReportCopyGaps["example-router"] == "" || x.sends != 0 {
		t.Fatal("failed copy erased data or replayed an investigation")
	}
	os.Remove(mode)
	os.Remove(local)
	os.WriteFile(f.sourceMode, []byte("empty"), 0600)
	e.nextReportCheck = time.Time{}
	if err := e.refreshReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(peer)
	if !bytes.Equal(prior, after) || e.Store.snapshot().ReportCopyGaps["example-router"] == "" {
		t.Fatal("empty local candidate erased a nonempty peer report")
	}
}
