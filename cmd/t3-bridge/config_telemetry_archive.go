package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

const workspaceTelemetryArchiveLines = 100

func runConfigTelemetryArchive(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config telemetry-archive", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "saved "+defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "select an App ID (defaults to the primary account)")
	date := flags.String("date", time.Now().UTC().Format(receiverTelemetryArchiveDate), "UTC archive date (YYYY-MM-DD, defaults to today)")
	lines := flags.Int("lines", workspaceTelemetryArchiveLines, "maximum number of matching records, from 1 to 1000")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	day, err := parseReceiverTelemetryArchiveDate(*date)
	if err != nil {
		return fmt.Errorf("-date: %w", err)
	}
	if *lines < 1 || *lines > serviceLogMaxLines {
		return errors.New("-lines must be between 1 and 1000")
	}
	config, err := loadConfigAccount(*path, *profile, *appID)
	if err != nil {
		return err
	}
	data, err := readReceiverTelemetryArchive(config.StateDir, newReceiverGroupAccount(config.Tenant, config.AppID), day, *lines)
	if err != nil || len(data) == 0 {
		return err
	}
	_, err = output.Write(data)
	return err
}

func parseReceiverTelemetryArchiveDate(raw string) (time.Time, error) {
	day, err := time.Parse(receiverTelemetryArchiveDate, raw)
	if err != nil || day.Year() < 1 || day.Format(receiverTelemetryArchiveDate) != raw {
		return time.Time{}, errors.New("expected a UTC date in YYYY-MM-DD format")
	}
	return day, nil
}

func lookupWorkspaceTelemetryArchive(path, appID, revision string, date time.Time) ([]byte, error) {
	raw, err := bridge.ReadRegularConfigJSON(path)
	if revision != "" && (err != nil || configRevision(raw) != revision) {
		return nil, errWorkspaceServiceConfigChanged
	}
	if err != nil {
		return nil, err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	account, err := selectConfigAccount(config, appID)
	if err != nil {
		return nil, err
	}
	return readReceiverTelemetryArchive(account.StateDir, newReceiverGroupAccount(account.Tenant, account.AppID), date, workspaceTelemetryArchiveLines)
}

func readReceiverTelemetryArchive(stateDir string, account receiverProcessAccount, date time.Time, lines int) ([]byte, error) {
	day := date.UTC().Format(receiverTelemetryArchiveDate)
	dir := receiverTelemetryArchiveDir(stateDir)
	if err := localfile.CheckPrivateDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	file, err := localfile.OpenPrivateSingleLink(filepath.Join(dir, receiverTelemetryArchiveName(day)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := min(info.Size(), serviceLogMaxBytes+1)
	raw, err := io.ReadAll(io.NewSectionReader(file, info.Size()-size, size))
	if err != nil {
		return nil, err
	}
	if size > serviceLogMaxBytes {
		// Check the preceding byte before including the first line of the tail.
		_, raw, _ = bytes.Cut(raw, []byte{'\n'})
	}
	var filtered bytes.Buffer
	encoder := json.NewEncoder(&filtered)
	for len(raw) != 0 {
		line, rest, complete := bytes.Cut(raw, []byte{'\n'})
		if !complete {
			break
		}
		raw = rest
		if len(line) >= serviceTelemetryMaxLineBytes {
			continue
		}
		record, ok := parseServiceTelemetry(line)
		if ok && record.receiverProcessAccount == account && record.At.Format(receiverTelemetryArchiveDate) == day {
			_ = encoder.Encode(record) // Validated fields written to a bytes.Buffer.
		}
	}
	return serviceLogTail(filtered.Bytes(), lines), nil
}
