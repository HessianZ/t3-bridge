//go:build !windows

package main

import "io"

func runHostService(args []string, output io.Writer) error {
	return runService(args, output)
}

func prepareHostReceiver(args []string) ([]string, bool, error) {
	return args, false, nil
}
