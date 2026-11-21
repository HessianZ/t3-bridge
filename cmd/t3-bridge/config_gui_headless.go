//go:build (!darwin || !cgo) && (!linux || !cgo || !desktop) && (!windows || !desktop)

package main

import "errors"

func runConfigGUI([]string) error {
	return errors.New("desktop configuration requires a native Mac build or a Linux or Windows desktop build; run t3-bridge config web with this binary")
}
