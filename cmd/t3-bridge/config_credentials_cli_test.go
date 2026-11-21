package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigCredentialsCLIManagement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.json")
	secretPath := filepath.Join(root, "credentials", "app.secret")
	writeCredentialConfig(t, path, secretPath, filepath.Join(root, "token"))
	const value = "synthetic-cli-management-secret"
	const appID = "cli_fixture"
	var out bytes.Buffer
	run := func(action, input string, args ...string) error {
		out.Reset()
		return runConfig(append([]string{"credentials", action, "-config", path}, args...), strings.NewReader(input), &out)
	}

	for _, tc := range []struct {
		name, input string
		args        []string
	}{
		{"missing app", value, nil},
		{"unconfigured app", value, []string{"-app-id", "cli_other"}},
		{"duplicate flag", value, []string{"-app-id", "cli_other", "-app-id", appID}},
		{"oversize", strings.Repeat("x", maxAppCredentialBytes+1), []string{"-app-id", appID}},
		{"multiline", value + "\nother", []string{"-app-id", appID}},
		{"unsupported flag", "", []string{"-app-id", appID, "-secret=" + value}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run("set", tc.input, tc.args...)
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), value) || fixtureAppCredential(t, path).Status != "missing" {
				t.Fatal("invalid set changed credential state, succeeded, or disclosed input")
			}
		})
	}
	if err := run("set", value+"\n", "-app-id", appID); err != nil || strings.Contains(out.String(), value) {
		t.Fatalf("set failed or disclosed the credential: %v", err)
	}
	if err := run("list", ""); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	want := []map[string]any{{"appId": appID, "tenant": "feishu", "path": secretPath, "status": "ready", "editable": true}}
	if err := json.Unmarshal(out.Bytes(), &items); err != nil || !reflect.DeepEqual(items, want) {
		t.Fatal("list did not return exactly the five metadata fields")
	}
	if err := run("remove", "", "-app-id", appID, "-confirm=false"); err == nil || out.Len() != 0 {
		t.Fatal("unconfirmed removal succeeded or returned a receipt")
	}
	if got, err := fixtureAppCredential(t, path).value(); err != nil || got != value {
		t.Fatal("the original value could not be read after unconfirmed removal")
	}
	if err := run("remove", "", "-app-id", appID, "-confirm"); err != nil || strings.Contains(out.String(), value) {
		t.Fatalf("confirmed removal failed or disclosed the credential: %v", err)
	}
	if got := fixtureAppCredential(t, path).Status; got != "missing" {
		t.Fatal("confirmed removal did not clear the credential file")
	}
	request := `{"protocolVersion":1,"provider":"t3-bridge","ids":["app-cli_fixture"]}`
	if err := run("get", request); err != nil || strings.TrimSpace(out.String()) != `{"protocolVersion":1,"values":{},"errors":{"app-cli_fixture":{"message":"not found"}}}` {
		t.Fatalf("missing credential did not produce a fixed per-ID error: %v", err)
	}
}

func TestConfigCredentialsCLIGet(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "USERPROFILE", "AppData"} {
		t.Setenv(key, root)
	}
	const id = "app-cli_fixture"
	const other = "app-cli_other"
	const unsupported = "token-cli_fixture"
	const version = 1
	const request = `{"protocolVersion":1,"provider":"lark-channel","ids":["app-cli_fixture","app-cli_other","token-cli_fixture","app-cli_fixture"]}`
	const value = "synthetic-provider-value"
	const namedValue = "synthetic-named-provider-value"
	path := filepath.Join(root, "fixture.json")
	namedSource := filepath.Join(root, "named.json")
	for source, secret := range map[string]string{path: value, namedSource: namedValue} {
		secretPath := filepath.Join(root, "credentials", filepath.Base(source)+".secret")
		writeCredentialConfig(t, source, secretPath, filepath.Join(root, "token"))
		if err := fixtureAppCredential(t, source).update("set", secret); err != nil {
			t.Fatal(err)
		}
	}
	if err := runProfile([]string{"create", "named", "-from", namedSource}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := runProfile([]string{"use", "named"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input string
		args        []string
		values      map[string]string
		missing     []string
	}{
		{"explicit file", request, []string{"-config", path}, map[string]string{id: value}, []string{other, unsupported}},
		{"explicit profile", strings.Replace(request, "lark-channel", "t3-bridge", 1), []string{"-profile", "named"}, map[string]string{id: namedValue}, []string{other, unsupported}},
		{"narrowed app", request, []string{"-config", path, "-app-id", "cli_other"}, map[string]string{}, []string{id, other, unsupported}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runConfigCredentials(append([]string{"get"}, tc.args...), strings.NewReader(tc.input), &out); err != nil {
				t.Fatal(err)
			}
			var got struct {
				ProtocolVersion int                          `json:"protocolVersion"`
				Values          map[string]string            `json:"values"`
				Errors          map[string]map[string]string `json:"errors"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ProtocolVersion != version || !reflect.DeepEqual(got.Values, tc.values) {
				t.Fatal("v1 response returned unexpected values or version")
			}
			want := make(map[string]map[string]string)
			for _, id := range tc.missing {
				want[id] = map[string]string{"message": "not found"}
			}
			if !reflect.DeepEqual(got.Errors, want) {
				t.Fatal("v1 response did not return fixed per-ID errors")
			}
		})
	}

	for _, tc := range []struct {
		name, input string
		args        []string
	}{
		{"implicit active profile", request, nil},
		{"both selectors", request, []string{"-config", path, "-profile", "named"}},
		{"version", strings.Replace(request, `"protocolVersion":1`, `"protocolVersion":2`, 1), []string{"-config", path}},
		{"provider", strings.Replace(request, "lark-channel", value, 1), []string{"-config", path}},
		{"malformed JSON", request + value, []string{"-config", path}},
		{"oversize", request + strings.Repeat(" ", maxCredentialRequestBytes), []string{"-config", path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runConfigCredentials(append([]string{"get"}, tc.args...), strings.NewReader(tc.input), &out)
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), value) {
				t.Fatal("invalid request succeeded, wrote a response, or disclosed input")
			}
		})
	}
}

func TestTopLevelSecretsCLI(t *testing.T) {
	if err := checkEntry(t, "secrets", "get"); err == nil || !strings.Contains(err.Error(), "requires explicit -config or -profile") {
		t.Fatalf("top-level secrets entry did not reach the common command: %v", err)
	}
}
