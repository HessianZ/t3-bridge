package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
	qrcode "github.com/skip2/go-qrcode"
)

const terminalBindingPoll = 250 * time.Millisecond

func runConfigBindFeishu(args []string, input io.Reader, status io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config bind-feishu", flag.ContinueOnError)
	flags.SetOutput(status)
	config := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	draftInput := flags.Bool("draft-stdin", false, "read the configuration draft from stdin")
	comments := flags.Bool("comments", false, "also request access to supported document comment events")
	var replaceAppID string
	flags.Func("replace-app-id", "replace this configured App with a different scanned App in the candidate", func(id string) error {
		if !validReplacementAppID(id) {
			return errors.New("replace-app-id requires a complete configured App ID")
		}
		replaceAppID = id
		return nil
	})
	output := flags.String("output", "", "required path to a new candidate JSON file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if !strings.EqualFold(filepath.Ext(*output), ".json") {
		return errors.New("bind-feishu requires -output with a new .json path")
	}
	if _, err := os.Lstat(*output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("bind-feishu requires an unused candidate path")
	}
	var raw []byte
	var err error
	if *draftInput {
		raw, err = readConfigDraft(input, *config, *profile)
	} else {
		path, resolveErr := resolveConfigPath(*config, *profile, defaultConfigPath)
		if resolveErr != nil {
			return resolveErr
		}
		raw, err = bridge.ReadConfigJSON(path)
	}
	if err != nil {
		return err
	}
	if replaceAppID != "" {
		if err := validateReplacementBinding(raw, replaceAppID); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(status, "Replacing %s in the candidate. The scanned App starts disabled, with its own state directory and the source App's default T3 route.\n", replaceAppID); err != nil {
			return err
		}
	} else if _, err := bridge.ParseConfig(raw); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	q := &qrRegistration{ctx: ctx, register: registration.RegisterApp}
	candidate, err := bindFeishuTerminal(q, raw, *comments, replaceAppID, status)
	if err != nil {
		return err
	}
	if _, err := saveConfigCandidate(*output, candidate, "missing"); err != nil {
		return err
	}
	_, err = fmt.Fprintln(status, "Candidate saved:", *output)
	return err
}

func bindFeishuTerminal(q *qrRegistration, raw []byte, comments bool, replaceAppID string, status io.Writer) ([]byte, error) {
	id := q.startFor(comments, replaceAppID)
	tick := time.NewTicker(terminalBindingPoll)
	defer tick.Stop()
	lastURL := ""
	for {
		if err := q.ctx.Err(); err != nil {
			return nil, err
		}
		view, _ := q.status(id)
		switch view.State {
		case "scanned":
			if replaceAppID != "" {
				if _, err := fmt.Fprintf(status, "Scanned %s to replace %s.\n", view.AppID, replaceAppID); err != nil {
					return nil, err
				}
			}
			return q.apply(raw, id)
		case "pending":
			if view.URL != "" && view.URL != lastURL {
				lastURL = view.URL
				if _, err := fmt.Fprintln(status, "Scan in Feishu or Lark, or open the authorization page:\n"+view.URL+"\n"+terminalQRCode(view.URL)); err != nil {
					return nil, err
				}
			}
		case "starting":
		default:
			return nil, errors.New("Feishu app authorization did not complete")
		}
		select {
		case <-q.ctx.Done():
			return nil, q.ctx.Err()
		case <-tick.C:
		}
	}
}

func terminalQRCode(address string) string {
	qr, err := qrcode.New(address, qrcode.Medium)
	if err != nil {
		return ""
	}
	return qr.ToSmallString(false)
}
