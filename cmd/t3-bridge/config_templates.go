package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"

	"github.com/HessianZ/t3-bridge/config"
)

func loadConfigTemplate(name string) ([]byte, error) {
	raw, err := config.Examples.ReadFile(name + ".example.json")
	if err != nil || (runtime.GOOS != "windows" && name == "homeops") {
		return raw, err
	}
	profiles, err := localProfiles()
	if err != nil {
		if runtime.GOOS != "windows" {
			return raw, nil
		}
		return nil, err
	}
	token := "t3-oauth.json"
	if name == "homeops" {
		token = "t3-token.secret"
	}
	credentials, _ := json.Marshal(filepath.Join(profiles.root, "credentials", token))
	raw = bytes.ReplaceAll(raw, []byte(`"/etc/t3-bridge/t3-token.secret"`), credentials)
	if runtime.GOOS != "windows" {
		return raw, nil
	}
	for old, native := range map[string]string{
		"/var/lib/t3-bridge":                  filepath.Join(profiles.root, "state", "default"),
		"/etc/t3-bridge/app.secret":           filepath.Join(profiles.root, "credentials", "app.secret"),
		"/etc/t3-bridge/monitor-hmac.secret":  filepath.Join(profiles.root, "credentials", "monitor-hmac.secret"),
		"/usr/local/bin/homeops-coordination": filepath.Join(profiles.root, "bin", "homeops-coordination.exe"),
	} {
		// These are exact quoted placeholders from the embedded examples.
		quoted, _ := json.Marshal(native)
		raw = bytes.ReplaceAll(raw, []byte(`"`+old+`"`), quoted)
	}
	return raw, nil
}
