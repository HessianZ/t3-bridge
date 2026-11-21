package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type userGroupsProfile struct {
	CurrentApp string            `json:"currentApp"`
	Apps       []json.RawMessage `json:"apps"`
}

type userGroupsSecretRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// The CLI can resolve a private file reference directly. Keep its copy of
// the app configuration separate from the operator's ordinary CLI profile.
func (g *UserGroups) readCLIProfile() (userGroupsProfile, userGroupsSecretRef, error) {
	var profile userGroupsProfile
	var app struct {
		AppID     string              `json:"appId"`
		Brand     string              `json:"brand"`
		AppSecret userGroupsSecretRef `json:"appSecret"`
	}
	file, err := localfile.OpenPrivate(filepath.Join(g.dir, "config.json"))
	if err != nil {
		return profile, app.AppSecret, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil || len(data) > MaxConfigBytes || json.Unmarshal(data, &profile) != nil ||
		len(profile.Apps) != 1 || json.Unmarshal(profile.Apps[0], &app) != nil ||
		app.AppID != g.account.AppID || app.Brand != g.account.Tenant || app.AppSecret.Source != "file" ||
		!filepath.IsAbs(app.AppSecret.ID) {
		return userGroupsProfile{}, userGroupsSecretRef{}, errors.New("无法核对群管理的私有 CLI 配置")
	}
	return profile, app.AppSecret, nil
}

func (g *UserGroups) prepareCLIProfile() error {
	profile, previous, err := g.readCLIProfile()
	create := errors.Is(err, os.ErrNotExist)
	if err != nil && !create {
		return err
	}
	if _, err := Secret(g.account.AppSecretFile); err != nil {
		return err
	}
	secret := userGroupsSecretRef{Source: "file", ID: g.account.AppSecretFile}
	if previous == secret {
		return nil
	}
	encodedSecret, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	var app map[string]json.RawMessage
	if create {
		name, _ := json.Marshal(g.account.AppID)
		tenant, _ := json.Marshal(g.account.Tenant)
		app = map[string]json.RawMessage{"name": name, "appId": name, "brand": tenant, "users": json.RawMessage("[]")}
		profile = userGroupsProfile{CurrentApp: g.account.AppID, Apps: make([]json.RawMessage, 1)}
	} else if err := json.Unmarshal(profile.Apps[0], &app); err != nil {
		return err
	}
	app["appSecret"] = encodedSecret
	profile.Apps[0], err = json.Marshal(app)
	if err != nil {
		return err
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	_, err = localfile.WritePrivateAtomic(filepath.Join(g.dir, "config.json"), data, create)
	return err
}
