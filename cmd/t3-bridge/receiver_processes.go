package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const receiverStopPollInterval = 200 * time.Millisecond

var receiverProcessID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type receiverProcessAccount struct {
	Tenant string `json:"tenant"`
	AppID  string `json:"appId"`
}

type receiverProcess struct {
	ID                string                   `json:"id"`
	PID               int                      `json:"pid"`
	StartedAt         time.Time                `json:"startedAt"`
	Version           string                   `json:"version,omitempty"`
	ConfigRevision    string                   `json:"configRevision,omitempty"`
	Accounts          []receiverProcessAccount `json:"accounts"`
	GroupAccess       bool                     `json:"groupAccess,omitempty"`
	GroupBatchGrant   bool                     `json:"groupBatchGrant,omitempty"`
	GroupMessageEvent bool                     `json:"groupMessageEvent,omitempty"`
	RunSettings       bool                     `json:"runSettings,omitempty"`
	AccessPolicy      bool                     `json:"accessPolicy,omitempty"`
	Reconnect         bool                     `json:"reconnect,omitempty"`
	ReconnectWait     bool                     `json:"reconnectWait,omitempty"`
	Meetings          bool                     `json:"meetings,omitempty"`
}

type receiverProcessRegistry struct {
	dir string
}

type receiverProcessRegistration struct {
	registry receiverProcessRegistry
	dir      string
	lock     *os.File
	process  receiverProcess
}

func localReceiverProcesses(profiles profileRegistry) receiverProcessRegistry {
	return receiverProcessRegistry{dir: filepath.Join(profiles.root, "receiver-processes")}
}

// Serialize publication, liveness probes and removal. Otherwise a concurrent
// probe could mistake another probe's brief lock on a stale entry for a receiver.
func (r receiverProcessRegistry) lock(wait bool) (*os.File, error) {
	if err := localfile.MkdirAllPrivate(r.dir); err != nil {
		return nil, err
	}
	return localfile.Lock(filepath.Join(r.dir, ".registry.lock"), wait)
}

func (r receiverProcessRegistry) registerConfig(accounts []bridge.Config, revision string) (*receiverProcessRegistration, error) {
	var random [16]byte
	rand.Read(random[:])
	process := receiverProcess{
		ID: hex.EncodeToString(random[:]), PID: os.Getpid(), StartedAt: time.Now().UTC(), Version: bridgeVersion(), ConfigRevision: revision,
		GroupAccess: true, GroupBatchGrant: true, GroupMessageEvent: true, RunSettings: true, AccessPolicy: true, Reconnect: true, ReconnectWait: true, Meetings: true,
	}
	for _, account := range accounts {
		process.Accounts = append(process.Accounts, newReceiverGroupAccount(account.Tenant, account.AppID))
	}
	data, err := json.Marshal(process)
	if err != nil {
		return nil, err
	}
	guard, err := r.lock(true)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	dir := filepath.Join(r.dir, process.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := localfile.Lock(filepath.Join(dir, "receiver.lock"), false)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(dir, "process.json"), data, true); err != nil {
		_ = lock.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &receiverProcessRegistration{registry: r, dir: dir, lock: lock, process: process}, nil
}

func (p *receiverProcessRegistration) close() error {
	guard, err := p.registry.lock(true)
	if err != nil {
		_ = p.lock.Close()
		return err
	}
	defer guard.Close()
	if err := p.lock.Close(); err != nil {
		return err
	}
	return os.RemoveAll(p.dir)
}

func (r receiverProcessRegistry) list() ([]receiverProcess, error) {
	guard, err := r.lock(true)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	var processes []receiverProcess
	for _, entry := range entries {
		if !entry.IsDir() || !receiverProcessID.MatchString(entry.Name()) {
			continue
		}
		process, err := r.activeProcess(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read receiver process %s: %w", entry.Name(), err)
		}
		processes = append(processes, process)
	}
	return processes, nil
}

// Call only while holding the registry lock. An unlocked entry is stale even
// when its recorded PID is alive, and a new receiver always uses a new ID.
func (r receiverProcessRegistry) activeProcess(id string) (receiverProcess, error) {
	dir := filepath.Join(r.dir, id)
	info, err := os.Lstat(dir)
	if err != nil {
		return receiverProcess{}, err
	}
	if !info.IsDir() {
		return receiverProcess{}, errors.New("invalid receiver process directory")
	}
	lock, err := localfile.Lock(filepath.Join(dir, "receiver.lock"), false)
	if err == nil {
		_ = lock.Close()
		return receiverProcess{}, os.ErrNotExist
	}
	if !errors.Is(err, localfile.ErrLocked) {
		return receiverProcess{}, err
	}
	f, err := localfile.OpenPrivate(filepath.Join(dir, "process.json"))
	if err != nil {
		return receiverProcess{}, err
	}
	defer f.Close()
	var process receiverProcess
	if err := json.NewDecoder(f).Decode(&process); err != nil {
		return receiverProcess{}, err
	}
	if process.ID != id {
		return receiverProcess{}, errors.New("receiver process ID does not match its registration")
	}
	return process, nil
}

// requestStop publishes the request without waiting for receiver shutdown.
func (r receiverProcessRegistry) requestStop(id string) error {
	if !receiverProcessID.MatchString(id) {
		return errors.New("use the complete process ID from t3-bridge ps")
	}
	guard, err := r.lock(true)
	if err != nil {
		return err
	}
	defer guard.Close()
	if _, err := r.activeProcess(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("receiver process %s is no longer active: %w", id, err)
		}
		return err
	}
	// Exclusive creation publishes an empty marker atomically in this ID's directory.
	path := filepath.Join(r.dir, id, "stop")
	f, err := localfile.CreatePrivate(path)
	if errors.Is(err, os.ErrExist) {
		f, err = localfile.OpenPrivate(path)
	}
	if err != nil {
		return err
	}
	return f.Close()
}

func (p *receiverProcessRegistration) watchStop(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(receiverStopPollInterval)
	defer ticker.Stop()
	path := filepath.Join(p.dir, "stop")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if f, err := localfile.OpenPrivate(path); err == nil {
				_ = f.Close()
				cancel()
				return
			}
		}
	}
}

func runReceiverProcessCommand(args []string, output io.Writer) error {
	const usage = "usage: t3-bridge ps | t3-bridge kill PROCESS_ID"
	if len(args) < 1 || (args[0] != "ps" && args[0] != "kill") {
		return errors.New(usage)
	}
	if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
		_, err := fmt.Fprintln(output, usage)
		return err
	}
	if (args[0] == "ps" && len(args) != 1) || (args[0] == "kill" && len(args) != 2) {
		return errors.New(usage)
	}
	profiles, err := localProfiles()
	if err != nil {
		return err
	}
	registry := localReceiverProcesses(profiles)
	if args[0] == "kill" {
		id := args[1]
		if err := registry.requestStop(id); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "Stop requested for receiver process %s. T3 work may continue. Managed startup settings still apply.\n", id); err != nil {
			return err
		}
		return registry.waitForStop(id, output)
	}
	processes, err := registry.list()
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		_, err := fmt.Fprintln(output, "No active receiver processes for this user.")
		return err
	}
	for _, process := range processes {
		apps := make([]string, 0, len(process.Accounts))
		for _, account := range process.Accounts {
			apps = append(apps, account.Tenant+"/"+account.AppID)
		}
		version := process.Version
		if version == "" {
			version = "unknown"
		}
		if _, err := fmt.Fprintf(output, "%s  pid=%d  started=%s  accounts=%q  groupAccess=%t  runSettings=%t  accessPolicy=%t  reconnect=%t  meetings=%t  version=%q\n", process.ID, process.PID, process.StartedAt.Format(time.RFC3339), strings.Join(apps, ", "), process.GroupAccess, process.RunSettings, process.AccessPolicy, process.Reconnect, process.Meetings, version); err != nil {
			return err
		}
	}
	return nil
}

func (r receiverProcessRegistry) waitForStop(id string, output io.Writer) error {
	ticker := time.NewTicker(receiverStopPollInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-timeout.C:
			return fmt.Errorf("stop requested for receiver process %s, but exit could not be confirmed within 10 seconds", id)
		case <-ticker.C:
			guard, err := r.lock(false)
			if errors.Is(err, localfile.ErrLocked) {
				continue
			}
			if err != nil {
				return err
			}
			_, err = r.activeProcess(id)
			_ = guard.Close()
			if errors.Is(err, os.ErrNotExist) {
				_, err := fmt.Fprintf(output, "Receiver process %s is no longer active.\n", id)
				return err
			}
			if err != nil {
				return err
			}
		}
	}
}
