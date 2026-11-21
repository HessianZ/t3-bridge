package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfigTest(t *testing.T, ext string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config"+ext)
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestStrictConfigPreservesDynamicKeysAndTargetIdentity(t *testing.T) {
	c := testConfig(t)
	c.Routes = map[string]Target{"Main-A": c.Routes["nas"]}
	c.DefaultRoute = "Main-A"
	r := c.Routes["Main-A"]
	r.Options = map[string]any{"ReasoningEffort": "high", "Nested": map[string]any{"X.Y": true, "x.y": false}}
	c.Routes["Main-A"] = r
	c.EventRoutes = map[string]string{"Node-A/security:Check": "Main-A"}
	expected := targetIdentity(r)
	t.Setenv("APPID", "should-not-win")
	t.Setenv("DEFAULTROUTE", "other")
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(writeConfigTest(t, ".json", b))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AppID != c.AppID || loaded.DefaultRoute != "Main-A" || loaded.EventRoutes["Node-A/security:Check"] != "Main-A" || !reflect.DeepEqual(loaded.Routes, c.Routes) || targetIdentity(loaded.Routes["Main-A"]) != expected {
		t.Fatal("config parser normalized an execution key/type or overrode config")
	}
}

func TestConfigRejectsAmbiguousOrSensitiveConfig(t *testing.T) {
	valid, err := json.Marshal(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, ext string
		b         []byte
	}{
		{"non-json-extension", ".txt", valid},
		{"wrong-type", ".json", []byte(`{"appId":["sensitive-value-DO-NOT-ECHO"]}`)},
		{"non-object", ".json", []byte("null")},
		{"invalid-utf8", ".json", []byte{0xff}},
		{"json-duplicate", ".json", []byte(`{"appId":"safe","appId":"sensitive-value-DO-NOT-ECHO"}`)},
		{"json-duplicate-escaped", ".json", []byte(`{"appId":"safe","\u0061ppId":"sensitive-value-DO-NOT-ECHO"}`)},
		{"json-double", ".json", append(append([]byte{}, valid...), []byte(" {}")...)},
		{"json-bad-case", ".json", []byte(strings.Replace(string(valid), `"appId"`, `"AppID"`, 1))},
		{"json-unknown", ".json", append([]byte(`{"secretInline":"sensitive-value-DO-NOT-ECHO",`), valid[1:]...)},
		{"json-deep", ".json", []byte(`{"routes":{"nas":{"options":{"x":` + strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70) + "}}}}")},
		{"json-big", ".json", []byte(strings.Repeat(" ", MaxConfigBytes+1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfigTest(t, tc.ext, tc.b))
			if err == nil || strings.Contains(err.Error(), "sensitive-value") {
				t.Fatal("bad input accepted or echoed", err)
			}
		})
	}
	// A failure cannot poison a later load.
	if _, err := LoadConfig(writeConfigTest(t, ".json", valid)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateMeetingSummaryTarget(t *testing.T) {
	primary := testConfig(t)
	secondary := testConfig(t)
	secondary.AppID = "synthetic-extra-app"
	route := secondary.Routes["mac"]
	route.InteractionMode = "plan"
	secondary.Routes["mac"] = route
	for _, tc := range []struct {
		name, target, route string
		enabled             bool
		wantErr             string
	}{
		{name: "disabled"},
		{name: "origin", target: "origin", route: "mac", enabled: true},
		{name: "owner", target: "owner", route: "mac", enabled: true},
		{name: "invalid", target: "Origin", route: "mac", enabled: true, wantErr: "meetingSummaryTarget must be empty, origin or owner"},
		{name: "meeting disabled", target: "owner", route: "mac", wantErr: "meetingSummaryTarget requires an enabled meeting and meetingRoute"},
		{name: "route missing", target: "origin", enabled: true, wantErr: "meetingSummaryTarget requires an enabled meeting and meetingRoute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := secondary
			account.MeetingSummaryTarget, account.MeetingRoute, account.MeetingEnabled = tc.target, tc.route, tc.enabled
			c := primary
			c.AdditionalAccounts = []Config{account}
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != "additional account: "+tc.wantErr {
				t.Fatalf("Validate() = %v, want %q for additional account", err, tc.wantErr)
			}
		})
	}
}
