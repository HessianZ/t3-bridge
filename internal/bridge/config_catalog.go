package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
)

type T3Catalog struct {
	EnvironmentID      string    `json:"environmentId"`
	LocalEnvironmentID string    `json:"localEnvironmentId"`
	LinkAccess         string    `json:"linkAccess,omitempty"`
	OperateAccess      string    `json:"operateAccess"`
	Label              string    `json:"label"`
	Projects           []Project `json:"projects"`
	SuggestedProjectID string    `json:"suggestedProjectId,omitempty"`
	T3Capabilities
}

func (c *T3Catalog) SuggestProject(projectID, workspacePath string) {
	c.SuggestedProjectID = ""
	if projectID != "" && projectID != "REPLACE_PROJECT_ID" {
		for _, project := range c.Projects {
			if project.ID == projectID {
				c.SuggestedProjectID = projectID
				return
			}
		}
		return
	}
	for _, project := range c.Projects {
		if !sameWorkspacePath(project.Root, workspacePath) {
			continue
		}
		if c.SuggestedProjectID != "" {
			c.SuggestedProjectID = ""
			return
		}
		c.SuggestedProjectID = project.ID
	}
}

type T3Capabilities struct {
	RuntimeMode string       `json:"runtimeMode"`
	Providers   []T3Provider `json:"providers"`
}

type T3Provider struct {
	ID            string    `json:"providerInstanceId"`
	Driver        string    `json:"driverKind"`
	Name          string    `json:"displayName"`
	RuntimeModes  []string  `json:"runtimeModes"`
	Models        []T3Model `json:"models"`
	OmittedModels int       `json:"omittedModels,omitempty"`
}

type T3Model struct {
	ID      string          `json:"id"`
	Label   string          `json:"label"`
	Options []T3ModelOption `json:"options"`
}

type T3ModelOption struct {
	ID     string `json:"id"`
	Values []any  `json:"values"`
}

// ValidateOptions uses JSON identity so a string such as "false" cannot
// stand in for a boolean advertised by T3.
func (m T3Model) ValidateOptions(options map[string]any) error {
	for key, value := range options {
		want, err := json.Marshal(value)
		if err != nil {
			return errors.New("model option unavailable")
		}
		valid := false
		for _, option := range m.Options {
			if option.ID != key {
				continue
			}
			for _, candidate := range option.Values {
				if raw, err := json.Marshal(candidate); err == nil && bytes.Equal(raw, want) {
					valid = true
					break
				}
			}
		}
		if !valid {
			return errors.New("model option unavailable")
		}
	}
	return nil
}

func (c MCPConfig) validateConnection() error {
	if (c.Endpoint == "") == (c.RuntimeFile == "") {
		return errors.New("configure exactly one of mcp.endpoint and mcp.runtimeFile")
	}
	if c.RuntimeFile != "" {
		if !filepath.IsAbs(c.RuntimeFile) {
			return errors.New("mcp.runtimeFile must be absolute")
		}
	} else if _, err := parseMCPEndpoint(c.Endpoint); err != nil {
		return err
	}
	if !filepath.IsAbs(c.TokenFile) {
		return errors.New("mcp.tokenFile must be absolute")
	}
	return nil
}

// DiscoverT3 reads projects and model capabilities using the same connection as
// the receiver. An unset environment ID allows the initial local discovery.
func DiscoverT3(ctx context.Context, c MCPConfig, environmentID, search string) (T3Catalog, error) {
	if err := c.validateConnection(); err != nil {
		return T3Catalog{}, err
	}
	client, err := ConnectMCP(ctx, c)
	if err != nil {
		return T3Catalog{}, err
	}
	defer client.Session.Close()
	return discoverT3(ctx, client, c.EnvironmentID, environmentID, search)
}

func discoverT3(ctx context.Context, caller ToolCaller, expectedLocal, environmentID, search string) (T3Catalog, error) {
	var catalog T3Catalog
	if err := caller.Call(ctx, "t3_environment_read", map[string]any{}, &catalog); err != nil {
		return T3Catalog{}, err
	}
	if catalog.EnvironmentID == "" || (expectedLocal != "" && catalog.EnvironmentID != expectedLocal) {
		return T3Catalog{}, errors.New("T3 MCP environment does not match the configured local environment")
	}
	local := catalog.EnvironmentID
	t3 := T3{Calls: caller}
	if environmentID != "" && environmentID != catalog.EnvironmentID {
		access, err := t3.linkedEnvironmentAccess(ctx, environmentID)
		if err != nil {
			return T3Catalog{}, err
		}
		catalog = T3Catalog{}
		if err := caller.Call(ctx, "t3_environment_read", map[string]any{"environmentId": environmentID}, &catalog); err != nil {
			return T3Catalog{}, err
		}
		if catalog.EnvironmentID != environmentID {
			return T3Catalog{}, errors.New("T3 target environment mismatch")
		}
		catalog.LinkAccess = access
	}
	catalog.LocalEnvironmentID = local
	catalog.OperateAccess = mcpOperateAccess(caller)
	projects, err := t3.Projects(ctx, Target{EnvironmentID: catalog.EnvironmentID})
	if err != nil {
		return T3Catalog{}, err
	}
	catalog.Projects = []Project{}
	for _, project := range projects {
		if project.ID != "" && project.ArchivedAt == nil && project.DeletedAt == nil {
			catalog.Projects = append(catalog.Projects, project)
		}
	}
	catalog.T3Capabilities, err = t3.capabilities(ctx, catalog.EnvironmentID, search)
	return catalog, err
}
