package main

import (
	"io"
	"strings"
	"testing"
)

func TestConfigUserGroupsRequiresExplicitInputs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"start", "-purpose", "other"}, "-purpose must be list or invite"},
		{[]string{"finish"}, "-ticket requires"},
		{[]string{"cancel"}, "-ticket requires"},
		{[]string{"invite", "-chat-id", "oc_example"}, "explicit -confirm"},
	} {
		args := append([]string{"my-groups"}, tc.args...)
		err := runConfig(args, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("runConfig(%q) = %v, want %q", args, err, tc.want)
		}
	}
}
