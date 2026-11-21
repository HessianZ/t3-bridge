package main

import "runtime/debug"

// The release script sets this version when linking the candidate binary.
var buildVersion string

func bridgeVersion() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}
