package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

func (r profileRegistry) runPurge(action, name string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge profile "+action+" NAME", flag.ContinueOnError)
	flags.SetOutput(output)
	preview := flags.Bool("preview", false, "print the paths and new default without deleting")
	revision := flags.String("revision", "", "revision from a previous preview")
	var confirm bool
	flags.BoolVar(&confirm, "confirm", false, "confirm permanent removal")
	flags.BoolVar(&confirm, "yes", false, "confirm permanent removal")
	purge := action == "purge"
	if action == "remove" {
		flags.BoolVar(&purge, "purge", false, "permanently remove this profile")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !purge || (*preview && confirm) {
		return errors.New(profileUsage)
	}
	if action == "remove" && !confirm && !*preview {
		return errors.New("profile remove --purge requires --yes, or use --preview")
	}
	plan, err := r.previewPurge(name)
	if err != nil {
		return err
	}
	if !confirm {
		return json.NewEncoder(output).Encode(plan)
	}
	if *revision != "" && *revision != plan.Revision {
		return errors.New("profile purge preview changed; review the paths again")
	}
	if err := r.applyPurge(name, plan.Revision); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, name)
	return err
}
