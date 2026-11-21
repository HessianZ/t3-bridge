package bridge

import (
	"context"
	"errors"
	"testing"
)

func metadataPageFor(target Target) map[string]any {
	p := pageFor(target)
	q := p["thread"].(map[string]any)
	q["title"] = "人工可读标题"
	q["settled"] = true
	q["activeRunId"] = nil
	q["latestRunId"] = "run"
	q["status"] = "completed"
	q["createdBy"] = "agent"
	q["creationSource"] = "mcp"
	q["parentThreadId"] = nil
	q["archived"] = false
	return p
}
func metadataCaller(t *testing.T, target Target, read func(map[string]any) map[string]any) scriptedCaller {
	t.Helper()
	return scriptedCaller(func(name string, a map[string]any) (any, error) {
		if a["environmentId"] != target.EnvironmentID {
			t.Fatal("metadata crossed environment", name)
		}
		switch name {
		case "t3_environment_read":
			return map[string]any{"environmentId": target.EnvironmentID}, nil
		case "t3_project_read":
			return map[string]any{"id": target.ProjectID}, nil
		case "orchestrator_capabilities":
			return map[string]any{"runtimeMode": target.RuntimeMode, "providers": []any{map[string]any{"providerInstanceId": target.ProviderID, "driverKind": target.Driver, "runtimeModes": []string{target.RuntimeMode}, "models": []any{map[string]any{"id": target.Model, "options": []any{map[string]any{"id": "effort", "values": []string{"high"}}}}}}}}, nil
		case "t3_thread_configure":
			return configurationFor(target), nil
		case "t3_thread_read":
			return read(a), nil
		default:
			t.Fatal("metadata read mutated thread", name)
			return nil, errors.New("unexpected")
		}
	})
}
func TestThreadMetadataNativeWireQuietAndIdentity(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	for _, bad := range []string{"", "active", "approval", "background", "waiting", "child", "foreign-run", "foreign-thread", "foreign-origin", "parent", "missing-active", "missing-pending", "missing-settled", "archived", "provider", "permissions", "options", "run-provider", "run-model", "executed-model"} {
		t.Run(bad, func(t *testing.T) {
			task := &Task{ThreadID: "thread", RunID: "run"}
			caller := metadataCaller(t, target, func(a map[string]any) map[string]any {
				p := metadataPageFor(target)
				q := p["thread"].(map[string]any)
				switch bad {
				case "active":
					q["activeRunId"] = "run"
					q["status"] = "running"
				case "approval":
					q["pendingRequestCount"] = 1
				case "background":
					q["pendingBackgroundTasks"] = []any{map[string]any{"kind": "command", "taskId": "cmd"}}
				case "waiting":
					q["blockedOnThreadWait"] = true
				case "foreign-run":
					q["latestRunId"] = "manual-run"
				case "foreign-thread":
					q["threadId"] = "manual-thread"
				case "foreign-origin":
					q["creationSource"] = "web"
					q["createdBy"] = "user"
				case "parent":
					q["parentThreadId"] = "foreign-parent"
				case "missing-active":
					delete(q, "activeRunId")
				case "missing-pending":
					delete(q, "pendingRequestCount")
				case "missing-settled":
					delete(q, "settled")
				case "archived":
					q["archived"] = true
				case "run-provider":
					p["recentRuns"].([]any)[0].(map[string]any)["providerInstanceId"] = "foreign"
				case "run-model":
					p["recentRuns"].([]any)[0].(map[string]any)["model"] = "foreign"
				case "executed-model":
					p["recentRuns"].([]any)[0].(map[string]any)["executedModel"] = "foreign"
				case "provider":
					q["providerInstanceId"] = "foreign"
				case "permissions":
					q["runtimeMode"] = "full-access"
				case "child":
					if a["threadId"] == "child" {
						q["threadId"] = "child"
						q["activeRunId"] = "child-run"
						q["status"] = "running"
					}
				}
				return p
			})
			api := &T3{Calls: scriptedCaller(func(n string, a map[string]any) (any, error) {
				if bad == "options" && n == "t3_thread_configure" {
					p := configurationFor(target)
					p["modelSelection"].(map[string]any)["options"] = map[string]any{"effort": "low"}
					return p, nil
				}
				var out any
				err := caller.Call(context.Background(), n, a, &out)
				return out, err
			})}
			if bad == "child" {
				task.ChildThreads = []string{"child"}
			}
			got, err := api.InspectThread(context.Background(), target, task)
			wantQuiet := bad == ""
			if (err == nil && got.Quiet) != wantQuiet {
				t.Fatal("native quiet/identity bypass", bad, got, err)
			}
		})
	}
}
func TestThreadMetadataPublicMutationContract(t *testing.T) {
	target := testConfig(t).Routes["nas"]
	task := &Task{ID: "task", ThreadID: "thread", RequestID: "request"}
	calls := 0
	api := &T3{Calls: scriptedCaller(func(n string, a map[string]any) (any, error) {
		calls++
		if a["environmentId"] != target.EnvironmentID || a["threadId"] != "thread" {
			t.Fatal("metadata target lost")
		}
		switch n {
		case "t3_thread_update":
			if a["action"] != "rename" || a["title"] != "磁盘调查" || a["clientRequestId"] != stable(task.ID, "title-v1", "磁盘调查") {
				t.Fatal(a)
			}
			return map[string]any{"threadId": "thread", "title": "磁盘调查"}, nil
		case "t3_thread_organize":
			if a["action"] != "settle" && a["action"] != "unsettle" {
				t.Fatal(a)
			}
			return map[string]any{}, nil
		default:
			t.Fatal("not a metadata call", n)
			return nil, nil
		}
	})}
	if err := api.RenameThread(context.Background(), target, task, "磁盘调查"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"settle", "unsettle"} {
		if err := api.OrganizeThread(context.Background(), target, task, action); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.OrganizeThread(context.Background(), target, task, "archive"); err == nil || calls != 3 {
		t.Fatal("archive was allowed")
	}
}
