package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const (
	receiverIPCTimeout        = 10 * time.Second
	receiverIPCPoll           = 50 * time.Millisecond
	receiverGroupTimeout      = receiverIPCTimeout
	receiverGroupCommandLimit = receiverIPCCommandLimit
	receiverIPCCommandLimit   = 4 << 10
	// The view can include up to ten pages of joined groups and local overrides.
	receiverGroupLimit = 2 << 20
)

var (
	errReceiverGroupBusy             = errors.New("a group access request is already pending for this account")
	errReceiverGroupUnsupported      = errors.New("receiver does not support live group access; restart it with an updated t3-bridge")
	errReceiverGroupBatchUnsupported = errors.New("receiver does not support batch group grants; restart it with an updated t3-bridge")
	errReceiverGroupUnknown          = errors.New("group access result is unknown")
	errReceiverGroupFailed           = errors.New("receiver could not complete the group access request")
	errReceiverGroupFile             = errors.New("invalid group access message")
)

// Both operations share the original group access routing envelope.
type receiverGroupRoute struct {
	Instance string                 `json:"instance"`
	Account  receiverProcessAccount `json:"account"`
	Ticket   string                 `json:"ticket"`
}

type receiverIPCCommand[Request any] struct {
	receiverGroupRoute
	Expires time.Time `json:"expires"`
	Request *Request  `json:"request"`
}

type receiverIPCReply[View any] struct {
	receiverGroupRoute
	View   *View `json:"view,omitempty"`
	Failed bool  `json:"failed,omitempty"`
}

type receiverGroupCommand = receiverIPCCommand[bridge.GroupAccessRequest]
type receiverGroupReply = receiverIPCReply[bridge.GroupAccessView]

type receiverIPC[Request, View any] struct {
	prefix, name                     string
	supported                        func(receiverProcess) bool
	prepareRequest                   func(*Request, receiverProcess)
	replyLimit                       int
	timeout                          time.Duration
	strict                           bool
	busy, unsupported, unknown, fail error
}

var receiverGroupIPC = receiverIPC[bridge.GroupAccessRequest, bridge.GroupAccessView]{
	prefix:    "group-access",
	name:      "group access",
	supported: func(p receiverProcess) bool { return p.GroupAccess },
	prepareRequest: func(request *bridge.GroupAccessRequest, p receiverProcess) {
		// Group IPC validates known field names in both directions.
		request.IncludeMessageEvent = p.GroupMessageEvent
	},
	replyLimit:  receiverGroupLimit,
	busy:        errReceiverGroupBusy,
	unsupported: errReceiverGroupUnsupported,
	unknown:     errReceiverGroupUnknown,
	fail:        errReceiverGroupFailed,
}

func newReceiverGroupAccount(tenant, appID string) receiverProcessAccount {
	if tenant == "" {
		tenant = "feishu"
	}
	return receiverProcessAccount{Tenant: tenant, AppID: appID}
}

func receiverGroupPendingPath(dir string, account receiverProcessAccount) string {
	return receiverGroupIPC.pendingPath(dir, account)
}

func (r receiverGroupRoute) returnDir(dir string) string {
	return receiverGroupIPC.returnDir(dir, r.Ticket)
}

func (r receiverProcessRegistry) groupAccess(ctx context.Context, id, tenant, appID string, request bridge.GroupAccessRequest) (bridge.GroupAccessView, error) {
	ipc := receiverGroupIPC
	if request.ChatIDs != nil {
		ipc.supported = func(p receiverProcess) bool { return p.GroupAccess && p.GroupBatchGrant }
		ipc.unsupported = errReceiverGroupBatchUnsupported
	}
	return ipc.exchange(ctx, r, id, newReceiverGroupAccount(tenant, appID), request)
}

func (p *receiverProcessRegistration) serveGroupAccess(ctx context.Context, account receiverProcessAccount, pending string, apply func(context.Context, bridge.GroupAccessRequest) (bridge.GroupAccessView, error)) {
	receiverGroupIPC.serve(ctx, p, account, pending, apply)
}

func (ipc receiverIPC[Request, View]) pendingPath(dir string, account receiverProcessAccount) string {
	hash := configRevision([]byte(account.Tenant + "\x00" + account.AppID))
	return filepath.Join(dir, ipc.prefix+"-"+hash+".json")
}

func (ipc receiverIPC[Request, View]) returnDir(dir, ticket string) string {
	return filepath.Join(dir, ipc.prefix+"-return-"+ticket)
}

func (ipc receiverIPC[Request, View]) exchange(ctx context.Context, r receiverProcessRegistry, id string, account receiverProcessAccount, request Request) (View, error) {
	var zero View
	if !receiverProcessID.MatchString(id) {
		return zero, errors.New("use the complete process ID from t3-bridge ps")
	}
	timeout := ipc.timeout
	if timeout == 0 {
		timeout = receiverIPCTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	expires, _ := ctx.Deadline()
	var ticket [16]byte
	rand.Read(ticket[:])
	command := receiverIPCCommand[Request]{
		receiverGroupRoute: receiverGroupRoute{
			Instance: id,
			Account:  account,
			Ticket:   hex.EncodeToString(ticket[:]),
		},
		Expires: expires,
		Request: &request,
	}
	ticker := time.NewTicker(receiverIPCPoll)
	defer ticker.Stop()
	for {
		err := ipc.enqueue(ctx, r, command)
		if err == nil {
			break
		}
		if !errors.Is(err, localfile.ErrLocked) {
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-ticker.C:
		}
	}
	dir := filepath.Join(r.dir, id)
	returnDir := ipc.returnDir(dir, command.Ticket)
	defer os.RemoveAll(returnDir)
	for {
		reply, err := readReceiverIPCFile[receiverIPCReply[View]](filepath.Join(returnDir, "reply.json"), ipc.replyLimit, ipc.strict)
		if err == nil {
			if reply.receiverGroupRoute != command.receiverGroupRoute || (reply.View == nil) != reply.Failed {
				return zero, ipc.unknown
			}
			if reply.Failed {
				return zero, ipc.fail
			}
			return *reply.View, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return zero, ipc.unknown
		}
		if _, err := os.Lstat(dir); err != nil {
			return zero, ipc.unknown
		}
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("%w: %w", ipc.unknown, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (ipc receiverIPC[Request, View]) enqueue(ctx context.Context, r receiverProcessRegistry, command receiverIPCCommand[Request]) error {
	guard, err := r.lock(false)
	if errors.Is(err, localfile.ErrLocked) {
		return localfile.ErrLocked
	}
	if err != nil {
		return errors.New("cannot lock receiver registry")
	}
	defer guard.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	process, err := r.activeProcess(command.Instance)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("receiver process is no longer active: %w", os.ErrNotExist)
	}
	if err != nil {
		return errors.New("cannot read receiver registration")
	}
	if !slices.Contains(process.Accounts, command.Account) {
		return errors.New("receiver process does not serve this account")
	}
	if !ipc.supported(process) {
		return ipc.unsupported
	}
	if ipc.prepareRequest != nil {
		ipc.prepareRequest(command.Request, process)
	}
	data, err := json.Marshal(command)
	if err != nil || len(data) > receiverIPCCommandLimit {
		return fmt.Errorf("invalid or oversized %s request", ipc.name)
	}
	dir := filepath.Join(r.dir, process.ID)
	pending := ipc.pendingPath(dir, command.Account)
	previous, err := readReceiverIPCFile[receiverIPCCommand[Request]](pending, receiverIPCCommandLimit, ipc.strict)
	if err == nil && ipc.ready(previous, dir, process.ID, command.Account) {
		return ipc.busy
	}
	if !errors.Is(err, os.ErrNotExist) {
		if err := os.Remove(pending); err != nil {
			return fmt.Errorf("cannot clear the previous %s request", ipc.name)
		}
	}
	returnDir := ipc.returnDir(dir, command.Ticket)
	if err := localfile.MkdirAllPrivate(returnDir); err != nil {
		return fmt.Errorf("cannot create %s response directory", ipc.name)
	}
	published, _ := localfile.WritePrivateAtomic(pending, data, true)
	if !published {
		_ = os.RemoveAll(returnDir)
		return fmt.Errorf("cannot publish %s request", ipc.name)
	}
	return nil
}

// Caller cancellation removes the return directory. Only its creator may
// create it, so a receiver returning late cannot resurrect it.
func (ipc receiverIPC[Request, View]) ready(c receiverIPCCommand[Request], dir, instance string, account receiverProcessAccount) bool {
	if c.Instance != instance || c.Account != account || !receiverProcessID.MatchString(c.Ticket) || c.Request == nil || !time.Now().Before(c.Expires) {
		return false
	}
	info, err := os.Lstat(ipc.returnDir(dir, c.Ticket))
	return err == nil && info.IsDir()
}

func (ipc receiverIPC[Request, View]) serve(ctx context.Context, p *receiverProcessRegistration, account receiverProcessAccount, pending string, apply func(context.Context, Request) (View, error)) {
	if ctx.Err() != nil {
		return
	}
	if _, err := os.Lstat(pending); err != nil {
		return
	}
	command, ok := ipc.claim(p, pending)
	if !ok || !ipc.ready(command, p.dir, p.process.ID, account) {
		return
	}
	ctx, cancel := context.WithDeadline(ctx, command.Expires)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	view, err := apply(ctx, *command.Request)
	reply := receiverIPCReply[View]{receiverGroupRoute: command.receiverGroupRoute, Failed: err != nil}
	if err == nil {
		reply.View = &view
	}
	data, err := json.Marshal(reply)
	if err == nil && len(data) <= ipc.replyLimit {
		_, _ = localfile.WritePrivateAtomic(filepath.Join(ipc.returnDir(p.dir, command.Ticket), "reply.json"), data, true)
	}
}

func (ipc receiverIPC[Request, View]) claim(p *receiverProcessRegistration, path string) (receiverIPCCommand[Request], bool) {
	guard, err := p.registry.lock(false)
	if err != nil {
		return receiverIPCCommand[Request]{}, false
	}
	defer guard.Close()
	command, err := readReceiverIPCFile[receiverIPCCommand[Request]](path, receiverIPCCommandLimit, ipc.strict)
	// Remove the pending command before applying it, including malformed or expired files.
	removed := os.Remove(path) == nil
	return command, removed && err == nil
}

func readReceiverGroupFile[T any](path string, limit int) (T, error) {
	return readReceiverIPCFile[T](path, limit, false)
}

func readReceiverIPCFile[T any](path string, limit int, strict bool) (T, error) {
	var zero T
	f, err := localfile.OpenPrivate(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(data) > limit || !utf8.Valid(data) {
		return zero, errReceiverGroupFile
	}
	var value T
	dec := json.NewDecoder(bytes.NewReader(data))
	if !scanReceiverIPCJSON(dec, reflect.TypeOf(&value).Elem(), strict) || json.Unmarshal(data, &value) != nil {
		return zero, errReceiverGroupFile
	}
	return value, nil
}

// encoding/json allows duplicate keys and case-insensitive field names.
// Check the IPC schema before typed decoding can discard those differences.
// The settings protocol also requires explicit non-null values and all
// non-optional fields, while older group access messages may omit values.
func scanReceiverIPCJSON(dec *json.Decoder, shape reflect.Type, strict bool) bool {
	token, err := dec.Token()
	if err != nil {
		return false
	}
	if token == nil {
		return !strict && (shape.Kind() == reflect.Pointer || shape.Kind() == reflect.Slice)
	}
	if shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	switch token {
	case json.Delim('{'):
		if shape.Kind() != reflect.Struct {
			return false
		}
		fields := reflect.VisibleFields(shape)
		seen := map[string]bool{}
		for dec.More() {
			name, err := dec.Token()
			key, ok := name.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			i := slices.IndexFunc(fields, func(f reflect.StructField) bool {
				tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				return f.IsExported() && tag != "" && tag == key
			})
			if i < 0 || !scanReceiverIPCJSON(dec, fields[i].Type, strict) {
				return false
			}
		}
		if strict {
			for _, field := range fields {
				tag, options, _ := strings.Cut(field.Tag.Get("json"), ",")
				if field.IsExported() && tag != "" && tag != "-" && options != "omitempty" && !seen[tag] {
					return false
				}
			}
		}
	case json.Delim('['):
		if shape.Kind() != reflect.Slice {
			return false
		}
		for dec.More() {
			if !scanReceiverIPCJSON(dec, shape.Elem(), strict) {
				return false
			}
		}
	default:
		return true
	}
	_, err = dec.Token()
	return err == nil
}
