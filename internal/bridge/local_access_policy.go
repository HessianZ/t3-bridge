package bridge

import (
	"context"
	"errors"
	"maps"
	"slices"
)

type LocalAccessPolicyRequest struct {
	Kind    string `json:"kind,omitempty"`
	UserID  string `json:"userId,omitempty"`
	RouteID string `json:"routeId,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type LocalAccessPolicyView struct {
	OwnerID  string                   `json:"ownerId"`
	Entries  []LocalAccessPolicyEntry `json:"entries"`
	RouteIDs []string                 `json:"routeIds,omitempty"`
}

// Effective is the permission for this ACL layer. Execution also checks the
// other ACL layers and whether the configured target is available.
type LocalAccessPolicyEntry struct {
	Kind       string `json:"kind"`
	UserID     string `json:"userId"`
	RouteID    string `json:"routeId,omitempty"`
	Configured bool   `json:"configured"`
	Override   string `json:"override"`
	Effective  bool   `json:"effective"`
}

var (
	errAccessOwnerUnknown = errors.New("Owner暂未解析，未撤销用户或管理员；请稍后核对。")
	errAccessOwnerRemoval = errors.New("不能删除或转移当前应用Owner。")
)

// LocalAccessPolicy runs on the receiver worker and uses its bound App state.
func (e *Engine) LocalAccessPolicy(ctx context.Context, request LocalAccessPolicyRequest) (LocalAccessPolicyView, error) {
	s := e.Store
	if s == nil || e.Config.AppID == "" {
		return LocalAccessPolicyView{}, errors.New("接收端没有绑定应用状态")
	}
	s.mu.Lock()
	appID := s.state.AccountID
	s.mu.Unlock()
	if appID != e.Config.AppID {
		return LocalAccessPolicyView{}, errors.New("接收端状态未绑定到当前应用")
	}
	if err := ctx.Err(); err != nil {
		return LocalAccessPolicyView{}, err
	}
	if (request != LocalAccessPolicyRequest{}) {
		if err := request.ValidateChange(); err != nil {
			return LocalAccessPolicyView{}, err
		}
		if request.Kind == "route" {
			if _, ok := e.Config.Routes[request.RouteID]; !ok {
				return LocalAccessPolicyView{}, errors.New("未知受控route，未改变权限。")
			}
		}
		if err := e.updateAccessPolicy(ctx, []LocalAccessPolicyRequest{request}, nil); err != nil {
			return LocalAccessPolicyView{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.AccessPolicy.localView(e.Config), nil
}

func (r LocalAccessPolicyRequest) ValidateChange() error {
	if !openIDPattern.MatchString(r.UserID) {
		return errors.New("用户 ID 必须为本应用的有效 ou_ 标识")
	}
	if r.Mode != "allow" && r.Mode != "deny" && r.Mode != "inherit" {
		return errors.New("授权模式必须为 allow、deny 或 inherit")
	}
	switch r.Kind {
	case "user", "admin":
		if r.RouteID != "" {
			return errors.New("用户或管理员授权不能指定路由")
		}
	case "route":
		if r.RouteID == "" {
			return errors.New("逐路由授权必须指定路由")
		}
	default:
		return errors.New("授权类型必须为 user、admin 或 route")
	}
	return nil
}

func (e *Engine) updateAccessPolicy(ctx context.Context, requests []LocalAccessPolicyRequest, record func(*State)) error {
	owner := e.Config.ownerID()
	published, err := e.Store.updatePublished(func(st *State) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, request := range requests {
			revoke, err := request.apply(&st.AccessPolicy, e.Config, owner)
			if err != nil {
				return err
			}
			if revoke {
				revokePending(e.Config, st, request.UserID, request.RouteID)
			}
		}
		if record != nil {
			record(st)
		}
		return nil
	})
	if published {
		e.publishPolicy()
	}
	return err
}

func (r LocalAccessPolicyRequest) apply(p *AccessPolicy, c Config, owner string) (bool, error) {
	var overrides map[string]bool
	var configured []string
	switch r.Kind {
	case "user":
		overrides, configured = p.Users, c.Users
	case "admin":
		overrides, configured = p.Admins, c.Admins
	case "route":
		overrides, configured = p.Routes[r.RouteID], c.Routes[r.RouteID].Users
	}
	revoke := r.Mode == "deny" || (r.Mode == "inherit" && overrides[r.UserID] && !slices.Contains(configured, r.UserID))
	if revoke && r.Kind != "route" {
		if owner == "" {
			return false, errAccessOwnerUnknown
		}
		if owner == r.UserID {
			if r.Mode == "deny" {
				return false, errAccessOwnerRemoval
			}
			revoke = false
		}
	}

	overrides = r.applyOverride(overrides)
	switch r.Kind {
	case "user":
		p.Users = overrides
	case "admin":
		p.Admins = overrides
	case "route":
		if len(overrides) == 0 {
			delete(p.Routes, r.RouteID)
		} else {
			if p.Routes == nil {
				p.Routes = map[string]map[string]bool{}
			}
			p.Routes[r.RouteID] = overrides
		}
	}
	return revoke && r.Kind != "admin", nil
}

func (r LocalAccessPolicyRequest) applyOverride(overrides map[string]bool) map[string]bool {
	if r.Mode == "inherit" {
		delete(overrides, r.UserID)
		return overrides
	}
	if overrides == nil {
		overrides = map[string]bool{}
	}
	overrides[r.UserID] = r.Mode == "allow"
	return overrides
}

func (p AccessPolicy) localView(c Config) LocalAccessPolicyView {
	owner := c.ownerID()
	entries := make([]LocalAccessPolicyEntry, 0)
	entries = appendAccessEntries(entries, "user", "", owner, c.Users, p.Users)
	entries = appendAccessEntries(entries, "admin", "", owner, c.Admins, p.Admins)
	routes := slices.Sorted(maps.Keys(c.Routes))
	for _, route := range routes {
		entries = appendAccessEntries(entries, "route", route, "", c.Routes[route].Users, p.Routes[route])
	}
	return LocalAccessPolicyView{OwnerID: owner, Entries: entries, RouteIDs: routes}
}

func appendAccessEntries(entries []LocalAccessPolicyEntry, kind, route, owner string, configured []string, overrides map[string]bool) []LocalAccessPolicyEntry {
	users := make(map[string]bool, len(configured)+len(overrides))
	for _, user := range configured {
		users[user] = true
	}
	for user := range overrides {
		if _, ok := users[user]; !ok {
			users[user] = false
		}
	}
	if owner != "" {
		if _, ok := users[owner]; !ok {
			users[owner] = false
		}
	}
	for _, user := range slices.Sorted(maps.Keys(users)) {
		entry := LocalAccessPolicyEntry{
			Kind: kind, UserID: user, RouteID: route, Configured: users[user],
			Override: "inherit", Effective: users[user],
		}
		if allowed, ok := overrides[user]; ok {
			entry.Override = "deny"
			if allowed {
				entry.Override = "allow"
			}
			entry.Effective = allowed
		}
		if owner != "" && user == owner {
			entry.Effective = true
		}
		entries = append(entries, entry)
	}
	return entries
}
