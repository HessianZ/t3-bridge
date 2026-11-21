package bridge

import (
	"context"
	"testing"
)

func TestDiscoverT3Catalog(t *testing.T) {
	caller := scriptedCaller(func(tool string, args map[string]any) (any, error) {
		switch tool {
		case "t3_environment_read":
			id := "local"
			if requested, ok := args["environmentId"].(string); ok {
				id = requested
			}
			return map[string]any{"environmentId": id, "label": "T3 " + id}, nil
		case "t3_environment_links":
			return map[string]any{"links": []map[string]any{{"environmentId": "remote", "status": "reachable", "access": "read-only"}}}, nil
		case "t3_project_list":
			if args["environmentId"] != "remote" {
				t.Fatal("project list used the wrong environment")
			}
			return map[string]any{"projects": []map[string]any{
				{"id": "open", "title": "Work", "workspaceRoot": "/work"},
				{"id": "archived", "archivedAt": "2026-01-01"},
			}}, nil
		case "orchestrator_capabilities":
			if args["environmentId"] != "remote" || args["search"] != "codex" {
				t.Fatal("capability query lost the target or search term")
			}
			return map[string]any{"runtimeMode": "approval-required", "providers": []T3Provider{
				{ID: "provider", Driver: "codex", Models: []T3Model{{ID: "codex", Options: []T3ModelOption{{ID: "Mixed.Case", Values: []any{false}}}}}},
			}}, nil
		}
		t.Fatalf("unexpected tool %s", tool)
		return nil, nil
	})
	catalog, err := discoverT3(context.Background(), caller, "local", "remote", "codex")
	if err != nil || catalog.LocalEnvironmentID != "local" || catalog.EnvironmentID != "remote" ||
		catalog.LinkAccess != "read-only" || catalog.RuntimeMode != "approval-required" ||
		len(catalog.Projects) != 1 || catalog.Projects[0].ID != "open" || len(catalog.Providers) != 1 {
		t.Fatalf("unexpected catalog: %+v, %v", catalog, err)
	}
	if option := catalog.Providers[0].Models[0].Options[0]; option.ID != "Mixed.Case" || option.Values[0] != false {
		t.Fatalf("model option changed: %+v", option)
	}
	if _, err := discoverT3(context.Background(), caller, "other", "remote", ""); err == nil {
		t.Fatal("expected local identity mismatch to reject discovery")
	}
	if _, err := discoverT3(context.Background(), caller, "local", "unlinked", ""); err != ErrRemoteUnavailable {
		t.Fatalf("unexpected error for unlinked environment: %v", err)
	}
}

func TestCatalogSuggestsOnlyOneProjectWithMatchingWorkspace(t *testing.T) {
	catalog := T3Catalog{Projects: []Project{
		{ID: "project_same_name", Root: "/elsewhere/work"},
		{ID: "project_match", Root: "/workspace/work"},
	}}
	catalog.SuggestProject("REPLACE_PROJECT_ID", "/workspace/./work/")
	if catalog.SuggestedProjectID != "project_match" {
		t.Fatal("unique path was not suggested")
	}
	catalog.Projects = append(catalog.Projects, Project{ID: "project_duplicate", Root: "/workspace/work"})
	catalog.SuggestProject("", "/workspace/work")
	if catalog.SuggestedProjectID != "" {
		t.Fatal("ambiguous workspace was suggested")
	}
	catalog.SuggestProject("project_same_name", "/workspace/work")
	if catalog.SuggestedProjectID != "project_same_name" {
		t.Fatal("existing selection was replaced")
	}
	catalog.SuggestProject("project_missing", "/elsewhere/work")
	if catalog.SuggestedProjectID != "" {
		t.Fatal("an unavailable explicit selection was replaced")
	}
}
