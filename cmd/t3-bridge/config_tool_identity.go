package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func runConfigToolIdentity(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config check-tool-identity", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "JSON configuration file")
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "select a configured app (defaults to the primary app)")
	route := flags.String("route", "", "required guarded route ID")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *route == "" {
		return errors.New("check-tool-identity requires -route and no positional arguments")
	}
	account, err := loadConfigAccount(*path, *profile, *appID)
	if err != nil {
		return err
	}
	if err := bridge.CheckToolIdentity(context.Background(), account, *route); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "local tool identity and read-only capability verified")
	return err
}
