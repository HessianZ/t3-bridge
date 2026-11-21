package bridge

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRunSettingsFormSchema(t *testing.T) {
	settings := RunSettingsView{
		MaxConcurrent: 0, IdleTimeoutMinutes: 120, Progress: true,
		ReplyMode: "markdown", ToolActivity: "default", ProcessMode: "stream-brief",
	}
	cmd := &SessionCommand{
		Source: Message{ID: "om_source"},
		Card: &CommandCard{
			AppID: "cli_example", Buttons: []CommandButton{{Label: "Save"}},
			RunSettingsForm: &RunSettingsForm{Settings: settings, RequireMention: false},
		},
	}
	type element struct {
		Tag            string `json:"tag"`
		Name           string `json:"name"`
		Elements       []element
		DefaultValue   string `json:"default_value"`
		InitialIndex   int    `json:"initial_index"`
		InputType      string `json:"input_type"`
		FormActionType string `json:"form_action_type"`
		Options        []struct{ Value string }
		Behaviors      []struct {
			Type  string
			Value map[string]string
		}
	}
	var card struct {
		Schema string
		Body   struct{ Elements []element }
	}
	if err := json.Unmarshal([]byte(commandCardJSON(cmd)), &card); err != nil {
		t.Fatal(err)
	}
	if card.Schema != "2.0" || len(card.Body.Elements) != 2 || card.Body.Elements[0].Tag != "div" || card.Body.Elements[1].Tag != "form" {
		t.Fatalf("expected a JSON 2.0 card with one form, got %+v", card)
	}
	form := card.Body.Elements[1]
	if form.Name == "" {
		t.Fatal("missing form name")
	}
	wantFields := map[string]struct {
		initial string
		options []string
	}{
		"max_concurrent":       {initial: "0"},
		"idle_timeout_minutes": {initial: "120"},
		"progress":             {"on", []string{"on", "off"}},
		"reply_mode":           {"markdown", []string{"card", "text", "markdown"}},
		"tool_activity":        {"default", []string{"default", "off", "brief", "detailed"}},
		"process_mode":         {"stream-brief", []string{"off", "brief", "detailed", "stream", "stream-brief", "stream-detailed", "card"}},
		"require_mention":      {"off", []string{"on", "off"}},
	}
	names := map[string]bool{form.Name: true}
	buttons := 0
	for _, item := range form.Elements {
		if item.Tag == "div" {
			continue
		}
		if item.Name == "" || names[item.Name] {
			t.Fatalf("missing or duplicate element name %q", item.Name)
		}
		names[item.Name] = true
		if item.Tag == "button" {
			buttons++
			want := map[string]string{"action": commandCardActionType, "id": commandButtonID(cmd, 0)}
			if item.FormActionType != "submit" || len(item.Behaviors) != 1 || item.Behaviors[0].Type != "callback" || !reflect.DeepEqual(item.Behaviors[0].Value, want) {
				t.Fatalf("invalid submit binding: %+v", item)
			}
			continue
		}
		want, ok := wantFields[item.Name]
		if !ok || len(item.Behaviors) != 0 {
			t.Fatalf("unexpected form field: %+v", item)
		}
		delete(wantFields, item.Name)
		if len(want.options) == 0 {
			if item.Tag != "input" || item.InputType != "text" || item.DefaultValue != want.initial {
				t.Fatalf("invalid number field: %+v", item)
			}
			continue
		}
		var options []string
		for _, option := range item.Options {
			options = append(options, option.Value)
		}
		if item.Tag != "select_static" || item.InitialIndex != slices.Index(want.options, want.initial)+1 || !slices.Equal(options, want.options) {
			t.Fatalf("invalid select field: %+v", item)
		}
	}
	if buttons != 1 || len(wantFields) != 0 {
		t.Fatalf("want one submit button and all fields, got %d buttons with missing fields %v", buttons, wantFields)
	}
}

func TestParseRunSettingsForm(t *testing.T) {
	maxConcurrent, idleTimeout, zero := 16, 120, 0
	on, off := true, false
	reply, tool, process := "markdown", "brief", "stream-detailed"
	for _, tc := range []struct {
		name   string
		values map[string]any
		want   RunSettingsFormSubmission
	}{
		{
			name: "all fields",
			values: map[string]any{
				"max_concurrent": "16", "idle_timeout_minutes": "120", "progress": "off", "reply_mode": "markdown",
				"tool_activity": "brief", "process_mode": "stream-detailed", "require_mention": "on",
			},
			want: RunSettingsFormSubmission{Settings: RunSettingsPatch{
				MaxConcurrent: &maxConcurrent, IdleTimeoutMinutes: &idleTimeout, Progress: &off,
				ReplyMode: &reply, ToolActivity: &tool, ProcessMode: &process,
			}, RequireMention: &on},
		},
		{
			name:   "explicit zero limits",
			values: map[string]any{"max_concurrent": "0", "idle_timeout_minutes": "0"},
			want:   RunSettingsFormSubmission{Settings: RunSettingsPatch{MaxConcurrent: &zero, IdleTimeoutMinutes: &zero}},
		},
		{
			name:   "only mention setting",
			values: map[string]any{"require_mention": "off"},
			want:   RunSettingsFormSubmission{RequireMention: &off},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRunSettingsForm(tc.values)
			if err != nil || !reflect.DeepEqual(got, &tc.want) {
				t.Fatalf("parseRunSettingsForm(%v) = %+v, %v, want %+v", tc.values, got, err, tc.want)
			}
		})
	}
}

func TestParseRunSettingsFormRejectsInvalidSubmissions(t *testing.T) {
	for name, values := range map[string]map[string]any{
		"absent":                nil,
		"empty":                 {},
		"unknown field":         {"progress": "on", "run_settings_submit": "on"},
		"too many fields":       {"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8"},
		"large value":           {"max_concurrent": strings.Repeat("0", 1024)},
		"number type":           {"max_concurrent": float64(1)},
		"boolean type":          {"progress": true},
		"null":                  {"progress": nil},
		"array":                 {"reply_mode": []any{"card"}},
		"empty string":          {"process_mode": ""},
		"whitespace":            {"max_concurrent": " 1 "},
		"negative integer":      {"max_concurrent": "-1"},
		"signed zero":           {"idle_timeout_minutes": "-0"},
		"leading plus":          {"idle_timeout_minutes": "+1"},
		"decimal":               {"idle_timeout_minutes": "1.5"},
		"scientific notation":   {"idle_timeout_minutes": "1e2"},
		"concurrency too high":  {"max_concurrent": "51"},
		"idle timeout too high": {"idle_timeout_minutes": "121"},
		"invalid progress":      {"progress": "true"},
		"invalid mention":       {"require_mention": "yes"},
		"invalid reply":         {"reply_mode": "html"},
		"invalid tools":         {"tool_activity": "stream"},
		"invalid process":       {"process_mode": "default"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseRunSettingsForm(values); err == nil || got != nil {
				t.Fatalf("parseRunSettingsForm(%v) = %+v, %v, want rejection", values, got, err)
			}
		})
	}
}
