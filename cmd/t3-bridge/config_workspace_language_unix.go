//go:build !windows && (!darwin || !cgo)

package main

func platformWorkspaceLanguage() workspaceLanguage { return workspaceAuto }
