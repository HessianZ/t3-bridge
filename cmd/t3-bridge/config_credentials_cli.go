package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

const credentialCLIUsage = "usage: t3-bridge {secrets|config credentials} {list|set|remove|get}"
const maxCredentialRequestBytes = 64 << 10

func runConfigCredentials(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New(credentialCLIUsage)
	}
	action := args[0]
	switch action {
	case "list", "set", "remove", "get":
	case "-h", "-help", "--help":
		_, err := fmt.Fprintln(output, credentialCLIUsage)
		return err
	default:
		return errors.New(credentialCLIUsage)
	}
	flags := flag.NewFlagSet("t3-bridge secrets "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "JSON configuration file (explicit -config or -profile required for get)")
	profile := flags.String("profile", "", "use the named configuration profile")
	var appID string
	var appFlags int
	flags.Func("app-id", "select one configured App ID (required for set and remove)", func(value string) error {
		appFlags++
		appID = value
		return nil
	})
	var confirm bool
	if action == "remove" {
		flags.BoolVar(&confirm, "confirm", false, "confirm removal of this App Secret")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(output, credentialCLIUsage)
			if action == "set" {
				fmt.Fprintln(output, "Read one App Secret from a masked prompt or redirected stdin.")
			}
			flags.SetOutput(output)
			flags.PrintDefaults()
			return nil
		}
		return errors.New("invalid credential command flags")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if appFlags > 1 || (appFlags == 1 && appID == "") {
		return errors.New("specify one nonempty -app-id")
	}
	if (action == "set" || action == "remove") && appID == "" {
		return errors.New("credential set and remove require -app-id")
	}
	if action == "remove" && !confirm {
		return errors.New("credential removal requires -confirm")
	}
	if action == "get" && *config == "" && *profile == "" {
		return errors.New("credential get requires explicit -config or -profile")
	}
	path, err := resolveConfigPath(*config, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	if action == "get" {
		return runCredentialGet(path, appID, input, output)
	}

	items, err := listAppCredentials(path)
	if err != nil {
		return err
	}
	if action == "list" && appID == "" {
		return json.NewEncoder(output).Encode(items)
	}
	item, err := selectAppCredential(items, appID)
	if err != nil {
		return err
	}
	if action == "list" {
		return json.NewEncoder(output).Encode([]appCredential{item})
	}
	var value string
	if action == "set" {
		value, err = readCredentialSetInput(input)
		if err != nil {
			return err
		}
	}
	if err := item.update(action, value); err != nil {
		return err
	}
	if action == "set" {
		_, err = fmt.Fprintln(output, "App Secret saved")
	} else {
		_, err = fmt.Fprintln(output, "App Secret removed")
	}
	return err
}

func readCredentialSetInput(input io.Reader) (string, error) {
	// The terminal password reader cannot enforce a byte limit while reading.
	if file, ok := input.(*os.File); ok && term.IsTerminal(file.Fd()) {
		return promptAppCredential(file)
	}
	raw, err := io.ReadAll(io.LimitReader(input, maxAppCredentialBytes+1))
	if err != nil {
		return "", errors.New("cannot read App Secret from stdin")
	}
	if len(raw) > maxAppCredentialBytes {
		return "", errors.New("App Secret input exceeds 64 KiB")
	}
	return string(raw), nil
}

func runCredentialGet(path, selectedAppID string, input io.Reader, output io.Writer) error {
	var request struct {
		ProtocolVersion int      `json:"protocolVersion"`
		Provider        string   `json:"provider"`
		IDs             []string `json:"ids"`
	}
	raw, err := io.ReadAll(io.LimitReader(input, maxCredentialRequestBytes+1))
	if err != nil || len(raw) > maxCredentialRequestBytes {
		return errors.New("cannot read credential request or request exceeds 64 KiB")
	}
	if json.Unmarshal(raw, &request) != nil || request.IDs == nil {
		return errors.New("invalid credential request JSON")
	}
	if request.ProtocolVersion != 1 {
		return errors.New("unsupported credential protocol version")
	}
	if request.Provider != "lark-channel" && request.Provider != "t3-bridge" {
		return errors.New("unsupported credential provider")
	}
	items, err := listAppCredentials(path)
	if err != nil {
		return err
	}

	type itemError struct {
		Message string `json:"message"`
	}
	response := struct {
		ProtocolVersion int                  `json:"protocolVersion"`
		Values          map[string]string    `json:"values"`
		Errors          map[string]itemError `json:"errors,omitempty"`
	}{ProtocolVersion: 1, Values: make(map[string]string), Errors: make(map[string]itemError)}
	for _, id := range request.IDs {
		if _, found := response.Values[id]; found {
			continue
		}
		if _, found := response.Errors[id]; found {
			continue
		}
		appID, isApp := strings.CutPrefix(id, "app-")
		if isApp && appID != "" && (selectedAppID == "" || appID == selectedAppID) {
			if item, err := selectAppCredential(items, appID); err == nil {
				if value, err := item.value(); err == nil {
					response.Values[id] = value
					continue
				}
			}
		}
		response.Errors[id] = itemError{Message: "not found"}
	}
	return json.NewEncoder(output).Encode(response)
}
