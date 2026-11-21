package bridge

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func TestProjectSetupOAuthBinding(t *testing.T) {
	const endpoint = "https://t3.example.invalid/mcp"
	const bearer = "synthetic-project-setup-token"
	root := t.TempDir()
	path := filepath.Join(root, "setup.json")
	saved := MCPConfig{Endpoint: endpoint, EnvironmentID: "env", TokenFile: filepath.Join(root, "runtime.json")}
	setup, err := saved.WithProjectSetupToken(path)
	if err != nil || setup.Endpoint != endpoint || setup.EnvironmentID != "env" || setup.TokenFile != path {
		t.Fatalf("setup connection: %+v, %v", setup, err)
	}
	for _, tt := range []struct {
		name, endpoint, fields, wantErr string
	}{
		{"bound", endpoint, `,"resource":"` + endpoint + `"`, ""},
		{"unbound HTTPS", endpoint, "", "requires an HTTPS resource binding"},
		{"other HTTPS endpoint", endpoint, `,"resource":"https://other.example.invalid/mcp"`, "bound to another"},
		{"local port", "http://127.0.0.1:3773/mcp", "", ""},
		{"expired", endpoint, `,"expiresAt":"2000-01-01T00:00:00Z"`, "expired"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value := []byte(`{"access_token":"` + bearer + `"` + tt.fields + `}`)
			if _, err := localfile.WritePrivateAtomic(path, value, false); err != nil {
				t.Fatal(err)
			}
			got, err := setup.bearer(tt.endpoint)
			if tt.wantErr == "" {
				if err != nil || got != bearer {
					t.Fatal("expected setup credential", err)
				}
			} else if got != "" || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected %q and no bearer, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestRegisterConfigProjectRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		local, target, directory, want string
	}{
		{"local", "", "/work", "目标 T3 环境"},
		{"local", "REPLACE_BUILD_ENVIRONMENT_ID", "/work", "目标 T3 环境"},
		{"REPLACE_ENVIRONMENT_ID", "target", "/work", "端点自身"},
		{"local", "target", "~/work", "绝对路径"},
		{"local", "target", `C:\REPLACE_PROJECT_PATH`, "目录占位值"},
	} {
		_, err := RegisterConfigProject(context.Background(), MCPConfig{EnvironmentID: tt.local}, tt.target, tt.directory)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("expected %q, got %v", tt.want, err)
		}
	}
}

func TestConfigProjectRegistrationSequence(t *testing.T) {
	type step struct {
		tool   string
		args   map[string]any
		result any
	}
	list := func(cursor int, next any, projects ...Project) step {
		if projects == nil {
			projects = []Project{}
		}
		return step{"t3_project_list", map[string]any{"environmentId": "remote", "cursor": float64(cursor), "limit": float64(100)},
			map[string]any{"projects": projects, "nextCursor": next}}
	}
	linked := func(access string) step {
		return step{"t3_environment_links", map[string]any{}, map[string]any{
			"links": []map[string]string{{"environmentId": "remote", "status": "reachable", "access": access}},
		}}
	}
	links := linked("full-access")
	identity := step{"t3_environment_read", map[string]any{"environmentId": "remote"}, map[string]string{"environmentId": "remote"}}
	project := Project{ID: "work", Root: "/srv/work", Title: "Verified"}
	read := step{"t3_project_read", map[string]any{"environmentId": "remote", "projectId": project.ID}, project}
	create := step{"t3_project_create", map[string]any{
		"environmentId": "remote", "workspaceRoot": project.Root, "title": "work", "createWorkspaceRootIfMissing": false,
	}, project}
	archivedAt := "2026-01-01"
	archived := Project{ID: "archived", Root: project.Root, ArchivedAt: &archivedAt}
	capability := func(mode string) step {
		return step{"orchestrator_capabilities", map[string]any{"environmentId": "local"}, map[string]string{"runtimeMode": mode}}
	}
	for _, tt := range []struct {
		name    string
		steps   []step
		wantErr string
	}{
		{"unlinked", []step{{links.tool, links.args, map[string]any{"links": []any{}}}}, "未关联或不可达"},
		{"identity mismatch", []step{links, {identity.tool, identity.args, map[string]string{"environmentId": "other"}}}, "核对关联环境的身份"},
		{"reuse from later page on read-only link", []step{linked("read-only"), identity,
			list(0, 100, Project{ID: "other", Root: "/elsewhere/work", Title: "work"}),
			list(100, nil, Project{ID: project.ID, Root: "/srv/./work/"}), read}, ""},
		{"archived", []step{links, identity, list(0, nil, archived)}, "已归档"},
		{"ambiguous", []step{links, identity, list(0, nil, project, archived)}, "多个 T3 项目"},
		{"malformed list", []step{links, identity, {"t3_project_list", list(0, nil).args,
			map[string]any{"projects": []Project{}}}}, "无法完整读取"},
		{"insufficient authorization", []step{links, identity, list(0, nil), capability("approval-required")}, "需要 T3 full-access"},
		{"missing authorization evidence", []step{links, identity, list(0, nil), capability("")}, "尚未提交项目登记"},
		{"insufficient linked access", []step{linked("approval-required"), identity, list(0, nil)}, "需要 T3 full-access 的关联权限"},
		{"create then verify", []step{links, identity, list(0, nil), capability("full-access"), create, read}, ""},
		{"lost creation response", []step{links, identity, list(0, nil), capability("full-access"), {create.tool, create.args, errors.New("private upstream error")}}, "结果未知"},
		{"readback mismatch", []step{links, identity, list(0, nil), capability("full-access"), create,
			{read.tool, read.args, Project{ID: "other", Root: project.Root}}}, "结果未知"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			caller := scriptedCaller(func(tool string, args map[string]any) (any, error) {
				if calls >= len(tt.steps) {
					t.Fatalf("unexpected call %s", tool)
				}
				want := tt.steps[calls]
				calls++
				if tool != want.tool || !maps.Equal(args, want.args) {
					t.Fatalf("got %s %v, want %s %v", tool, args, want.tool, want.args)
				}
				if err, ok := want.result.(error); ok {
					return nil, err
				}
				return want.result, nil
			})
			got, err := registerConfigProject(context.Background(), caller, "local", "remote", project.Root)
			if calls != len(tt.steps) {
				t.Fatalf("got %d calls, want %d", calls, len(tt.steps))
			}
			if tt.wantErr == "" {
				if err != nil || got != project {
					t.Fatalf("got %+v, %v", got, err)
				}
			} else if got != (Project{}) || err == nil || !strings.Contains(err.Error(), tt.wantErr) || strings.Contains(err.Error(), "private upstream error") {
				t.Fatalf("expected safe error containing %q, got %+v, %v", tt.wantErr, got, err)
			}
		})
	}
}
