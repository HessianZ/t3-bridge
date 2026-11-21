package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigFeishu(command string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config "+command, flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "select a configured app (defaults to the primary app)")
	var probeAvailability bool
	if command == "check-meeting" {
		flags.BoolVar(&probeAvailability, "probe-availability", false, "check the selected App owner's active meeting endpoint and report only availability")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	account, err := loadConfigAccount(*path, *profile, *appID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch command {
	case "check-meeting":
		check := bridge.CheckMeetingPermissions
		if probeAvailability {
			check = bridge.CheckMeetingAvailability
		}
		report, err := check(ctx, account)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(report)
	case "list-groups":
		groups, err := bridge.ListFeishuGroups(ctx, account.AppID, account.AppSecretFile, account.Tenant)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(groups)
	}
	if err := bridge.CheckFeishuApp(ctx, account.AppID, account.AppSecretFile, account.Tenant); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "application API verified")
	return err
}

func configFeishuCheck(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "无法读取应用信息", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := bridge.CheckFeishuApp(ctx, strings.TrimSpace(r.PostForm.Get("appId")),
		strings.TrimSpace(r.PostForm.Get("appSecretFile")), strings.TrimSpace(r.PostForm.Get("tenant"))); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
