package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type HealthSource struct {
	URL     string `json:"url"`
	Node    string `json:"node"`
	LocalIP string `json:"localIp"`
	KeyFile string `json:"keyFile"`
}

var nodeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
var checkName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func (h HealthSource) Validate() error {
	u, err := url.Parse(h.URL)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || u.User != nil || u.Path != "/healthz" || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(h.LocalIP) == nil || !nodeName.MatchString(h.Node) || !filepath.IsAbs(h.KeyFile) {
		return errors.New("health source requires fixed IP /healthz, explicit localIp, node and keyFile")
	}
	return nil
}

type Evidence struct {
	Title    string
	Evidence string
	Action   string
	Severity string
}
type Event struct {
	ID            string     `json:"event_id"`
	Schema        string     `json:"schema"`
	Node          string     `json:"node"`
	Check         string     `json:"check"`
	Phase         string     `json:"phase"`
	FailureStart  string     `json:"failure_start"`
	Observed      string     `json:"observed"`
	SourceSampled string     `json:"source_sampled"`
	Notice        string     `json:"notice"`
	Evidence      []Evidence `json:"evidence,omitempty"`
	Delivered     string     `json:"delivered,omitempty"`
}

func normalized(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.999999Z")
}
func eventID(node, check, start, phase string) string {
	h := sha256.Sum256([]byte("homeops.eventbridge/v1\n" + node + "\n" + check + "\n" + start + "\n" + phase))
	return "eb1_" + hex.EncodeToString(h[:])[:32]
}
func incidentKey(e Event) string { return "incident:" + e.Node + "/" + e.Check + "/" + e.FailureStart }
func RawKey(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return nil, errors.New("health key file missing or not private")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) != 32 {
		return nil, errors.New("health key must be exactly 32 raw bytes")
	}
	return b, nil
}
func ParseHealth(b []byte, signature string, key []byte, node string, now, floor time.Time) ([]Event, time.Time, error) {
	fail := func() ([]Event, time.Time, error) {
		return nil, time.Time{}, errors.New("health coverage gap: invalid signature, identity, freshness or payload")
	}
	sig, err := hex.DecodeString(signature)
	if err != nil || len(sig) != 32 || len(key) != 32 || len(b) > 32768 {
		return fail()
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return fail()
	}
	var p struct {
		Node        string               `json:"node"`
		Sampled     time.Time            `json:"sampled"`
		CollectorOK bool                 `json:"collector_ok"`
		Active      []string             `json:"active_incidents"`
		Starts      map[string]time.Time `json:"incident_starts"`
		Deliveries  []struct {
			Check     string    `json:"check"`
			Kind      string    `json:"kind"`
			Start     time.Time `json:"failure_start"`
			Observed  time.Time `json:"observed"`
			Delivered time.Time `json:"delivered"`
		} `json:"deliveries"`
		Attention []struct {
			Category string
			Title    string
			Evidence string
			Action   string
			Severity string
		} `json:"attention"`
	}
	if json.Unmarshal(b, &p) != nil || p.Node != node || !nodeName.MatchString(node) || !p.CollectorOK || p.Sampled.IsZero() || now.Sub(p.Sampled) > 150*time.Second || p.Sampled.Sub(now) > 30*time.Second || len(p.Active) > 32 || len(p.Starts) > 32 || len(p.Deliveries) > 32 || len(p.Attention) > 8 {
		return fail()
	}
	if floor.IsZero() {
		floor = p.Sampled
	}
	result := []Event{}
	create := func(check, phase string, start, observed, delivered time.Time) Event {
		e := Event{Schema: "homeops.eventbridge/v1", Node: node, Check: check, Phase: phase, FailureStart: normalized(start), Observed: normalized(observed), SourceSampled: normalized(p.Sampled), Notice: "Read-only monitoring event. Not a production authorization."}
		e.ID = eventID(node, check, e.FailureStart, phase)
		if !delivered.IsZero() {
			e.Delivered = normalized(delivered)
		}
		for _, a := range p.Attention {
			if a.Category == check {
				e.Evidence = append(e.Evidence, Evidence{display(a.Title, 240), display(a.Evidence, 240), display(a.Action, 240), display(a.Severity, 240)})
			}
		}
		return e
	}
	for _, check := range p.Active {
		start, ok := p.Starts[check]
		if !checkName.MatchString(check) || !ok || start.IsZero() || start.After(p.Sampled) {
			return fail()
		}
		result = append(result, create(check, "fault", start, p.Sampled, time.Time{}))
	}
	for _, d := range p.Deliveries {
		if !checkName.MatchString(d.Check) || !slices.Contains([]string{"fault", "recovery"}, d.Kind) || d.Start.IsZero() || d.Observed.Before(d.Start) || d.Delivered.Before(d.Observed) || d.Delivered.After(now.Add(30*time.Second)) {
			return fail()
		}
		if d.Kind == "recovery" && d.Delivered.After(floor) && !d.Delivered.Before(now.Add(-10*time.Minute)) {
			result = append(result, create(d.Check, "recovery", d.Start, d.Observed, d.Delivered))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, floor, nil
}
func FetchHealth(ctx context.Context, h HealthSource, now, floor time.Time) ([]Event, time.Time, error) {
	if err := h.Validate(); err != nil {
		return nil, floor, err
	}
	key, err := RawKey(h.KeyFile)
	if err != nil {
		return nil, floor, err
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(h.LocalIP)}}
	transport := &http.Transport{DialContext: dialer.DialContext}
	defer transport.CloseIdleConnections()
	client := privateHTTP(4 * time.Second)
	client.Transport = transport
	req, err := http.NewRequestWithContext(ctx, "GET", h.URL, nil)
	if err != nil {
		return nil, floor, errors.New("invalid health request")
	}
	r, err := client.Do(req)
	if err != nil {
		return nil, floor, errors.New("health source unavailable")
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, floor, errors.New("health source is not healthy")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 32769))
	if err != nil {
		return nil, floor, errors.New("health response unreadable")
	}
	return ParseHealth(b, r.Header.Get("X-HomeOps-Signature"), key, h.Node, now, floor)
}
func (e *Engine) pollHealth(ctx context.Context) error {
	if len(e.Config.Health) == 0 || (!e.lastHealth.IsZero() && e.now().Sub(e.lastHealth) < 30*time.Second) {
		return nil
	}
	e.lastHealth = e.now()
	for _, source := range e.Config.Health {
		st := e.Store.snapshot()
		floor, _ := time.Parse(time.RFC3339Nano, st.RecoveryFloors[source.Node])
		events, newFloor, err := FetchHealth(ctx, source, e.now(), floor)
		if err != nil {
			if saveErr := e.Store.update(func(st *State) error {
				st.HealthGaps[source.Node] = "健康来源覆盖缺口；未生成恢复事件"
				return nil
			}); saveErr != nil {
				return saveErr
			}
			continue
		} // A gap never resets the durable floor.
		if err = e.Store.update(func(st *State) error {
			delete(st.HealthGaps, source.Node)
			st.RecoveryFloors[source.Node] = newFloor.Format(time.RFC3339Nano)
			for _, event := range events {
				st.EventRecords[event.ID] = event
			}
			return e.enqueueAutomatic(st, source.Node, events)
		}); err != nil {
			return err
		}
	}
	return nil
}

var eventMarker = regexp.MustCompile(`\beb1_[A-Za-z0-9_]*`)

func eventBindingKey(m Message, event string) string { return "scope:" + userScope(m) + ":" + event }

func (e *Engine) inheritEvent(m Message, task *Task, st *State) (Message, error) {
	if m.EventID != "" {
		return m, nil
	}
	previous := replyForMessage(task, m).Source
	if previous.ID == "" {
		previous = task.Source
	}
	if previous.EventID == "" {
		return m, nil
	}
	event, ok := st.EventRecords[previous.EventID]
	if !ok || userScope(m) != userScope(task.Source) || previous.EventNode != event.Node || previous.EventCheck != event.Check || e.Config.eventRoute(event) != task.RouteID {
		return m, errors.New("原轮事件身份未确认或目标已变化，请明确引用与范围")
	}
	m.EventID, m.EventNode, m.EventCheck, m.EventRoute = event.ID, event.Node, event.Check, task.RouteID
	m.EventInherited = m.ParentID == "" && m.ControlTask == ""
	return m, nil
}
func bindEvent(st *State, m Message, task string) {
	for _, k := range []string{m.EventID, incidentKey(st.EventRecords[m.EventID])} {
		st.Events[eventBindingKey(m, k)] = task
		if old := st.Tasks[st.Events[k]]; old == nil || userScope(old.Source) == userScope(m) {
			st.Events[k] = task // Retain existing single-scope identities for upgrades.
		}
	}
}

func (e *Engine) linkEvent(m Message) (Message, error) {
	if !slices.Contains(e.Config.EventChats, m.ChatID) && !(m.ChatType == "group" && e.Config.access != nil && e.Config.groupAllowed(m.ChatID)) {
		return m, nil
	}

	ids := map[string]bool{}
	for _, id := range eventMarker.FindAllString(m.Quote, -1) {
		ids[id] = true
	}
	if len(ids) == 0 && !strings.Contains(m.Quote, "eb1_") {
		return m, nil
	}
	if len(ids) != 1 {
		return m, errors.New("请只引用一个已知告警事件")
	}
	var id string
	for candidate := range ids {
		id = candidate
	}
	st := e.Store.snapshot()
	event, ok := st.EventRecords[id]
	if !ok {
		return m, errors.New("引用事件尚未由可信健康来源确认，请明确目标与调查范围")
	}
	route := e.Config.eventRoute(event)
	if route == "" {
		return m, errors.New("引用事件未配置调查目标")
	}
	m.EventID = id
	m.EventNode = event.Node
	m.EventCheck = event.Check
	m.EventRoute = route
	return m, nil
}

type AgentReport struct {
	Stage   string
	Summary string
	Updated string
	Node    string
	Check   string
	EventID string `json:",omitempty"`
}

func (e *Engine) writeReports() error { return e.exportReports(context.Background()) }

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

func atomicPrivate(path string, b []byte) error {
	_, err := localfile.WritePrivateAtomic(path, b, false)
	return err
}
