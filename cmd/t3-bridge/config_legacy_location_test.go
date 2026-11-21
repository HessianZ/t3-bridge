package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLegacyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LARK_CHANNEL_HOME", "")
	if err := os.Unsetenv("LARK_CHANNEL_HOME"); err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	create := func(root string) string {
		t.Helper()
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "config.json")
		// Discovery should accept the path without parsing any contents.
		if err := os.WriteFile(path, []byte("synthetic non-JSON"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	check := func(want string) {
		t.Helper()
		if got, err := detectLegacyConfig(); err != nil || got != want {
			t.Fatalf("found %q, want %q: %v", got, want, err)
		}
	}
	if got, err := detectLegacyConfig(); got != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no installed config: %q %v", got, err)
	}
	check(create(filepath.Join(xdg, "lark-channel-bridge")))
	main := create(filepath.Join(home, ".lark-channel"))
	check(main)

	t.Run("explicit root", func(t *testing.T) {
		custom := filepath.Join(home, "custom")
		t.Setenv("LARK_CHANNEL_HOME", custom)
		if got, err := detectLegacyConfig(); got != "" || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing override must take precedence: %q %v", got, err)
		}
		check(create(custom))
		t.Setenv("LARK_CHANNEL_HOME", "relative")
		if got, err := detectLegacyConfig(); got != "" || err == nil {
			t.Fatalf("accepted a relative override: %q %v", got, err)
		}
	})

	t.Run("invalid primary", func(t *testing.T) {
		if err := os.Remove(main); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(main, 0700); err != nil {
			t.Fatal(err)
		}
		if got, err := detectLegacyConfig(); got != "" || err == nil {
			t.Fatalf("invalid primary must take precedence: %q %v", got, err)
		}
	})
}
