package main

import "io"

func runHostService(args []string, output io.Writer) error {
	return runWindowsService(args, output)
}
