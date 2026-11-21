package bridge

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkspaceOwnerSDKAndNewChatInheritsProject(t *testing.T) {
	e, _, ch, x, f := setupAccess(t)
	e.Config.DefaultRoute = "nas"
	projects := []Project{{ID: "homeops", Title: "管理", Root: "/main"}, {ID: "alpha", Title: "工作区", Root: "/srv/alpha"}}
	creates := 0
	api := &T3{Calls: scriptedCaller(func(name string, a map[string]any) (any, error) {
		if a["environmentId"] != e.Config.Routes["nas"].EnvironmentID {
			t.Fatal("wrong environment")
		}
		switch name {
		case "t3_project_list":
			return map[string]any{"projects": projects}, nil
		case "t3_project_read":
			for _, p := range projects {
				if p.ID == a["projectId"] {
					return p, nil
				}
			}
		case "t3_project_create":
			creates++
			if a["createWorkspaceRootIfMissing"] != false {
				t.Fatal("mkdir allowed")
			}
			p := Project{ID: "registered", Title: "登记工作区", Root: a["workspaceRoot"].(string)}
			projects = append(projects, p)
			return p, nil
		}
		t.Fatal("unexpected", name)
		return nil, nil
	})}
	e.T3 = &workspaceExecutor{fakeExecutor: x, api: api}
	// A normal allowed group member is not promoted to owner or admin.
	accessCommand(t, e, ch, "other-create", "other", "chat", "/cd /srv/new")
	if creates != 0 || !strings.Contains(f.replies[len(f.replies)-1], "owner/admin") {
		t.Fatal("ordinary member registered directory")
	}
	accessCommand(t, e, ch, "owner-create", "user", "chat", "/cd /srv/new")
	if creates != 1 {
		t.Fatal("official owner identity did not permit registration")
	}
	// Existing project reuse is allowed without registering or changing model/provider policy.
	accessCommand(t, e, ch, "owner-cd", "user", "chat", "/cd /srv/alpha")
	f.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/chats" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			members := body["user_id_list"].([]any)
			if len(members) != 1 || members[0] != "user" {
				t.Error("private chat membership changed")
			}
			io.WriteString(w, `{"code":0,"data":{"chat_id":"workspace-chat"}}`)
			return true
		}
		if r.Method == "POST" && r.URL.Path == "/open-apis/im/v1/messages" {
			io.WriteString(w, `{"code":0,"data":{"message_id":"welcome"}}`)
			return true
		}
		return false
	}
	accessCommand(t, e, ch, "new-chat", "user", "chat", "/new chat 工作区测试")
	m := groupMessage("new-input", "user", "workspace-chat", "/nas 只回复OK", true)
	receiveAccess(t, ch, m)
	tick(t, e)
	task := onlyTask(t, e)
	if task.Target.ProjectID != "alpha" || task.Target.Model != e.Config.Routes["nas"].Model || task.Target.RuntimeMode != e.Config.Routes["nas"].RuntimeMode {
		t.Fatal("new chat lost controlled project or execution policy", task.Target)
	}
	if e.Config.Routes["nas"].ProjectID != "homeops" {
		t.Fatal("global config changed")
	}
}
