package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type legacyCredentialPlan struct {
	SourcePath, Profile, AppID, Destination        string
	Inline                                         bool
	canonicalSource, sourceRevision, draftRevision string
}

func (request legacyConfigImport) credentialPlan(source, candidate []byte) (legacyCredentialPlan, error) {
	selection, err := selectLegacyImportSource(source, request.Profile)
	if err != nil {
		return legacyCredentialPlan{}, err
	}
	app, err := selection.app()
	if err != nil {
		return legacyCredentialPlan{}, err
	}
	if selection.singleAccount {
		if _, err := legacyLiteralAppSecret(app); err != nil {
			return legacyCredentialPlan{}, err
		}
	} else if profile := selection.name; profile == "" || profile != strings.TrimSpace(profile) || profile == "." || profile == ".." ||
		strings.ContainsAny(profile, "/\\\x00\r\n") {
		return legacyCredentialPlan{}, errors.New("复制凭据需要有效的旧配置名称")
	}
	sourcePath, err := filepath.Abs(request.SourcePath)
	if err != nil {
		return legacyCredentialPlan{}, errors.New("无法定位旧配置文件")
	}
	canonical, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return legacyCredentialPlan{}, errors.New("无法定位旧配置文件")
	}
	var account struct {
		AppID string `json:"appId"`
	}
	if err := json.Unmarshal(candidate, &account); err != nil {
		return legacyCredentialPlan{}, errors.New("无法读取候选配置的应用 ID")
	}
	if id, _, err := selection.appIdentity(app); err != nil || id != account.AppID {
		return legacyCredentialPlan{}, errors.New("候选配置的应用 ID 与旧配置不符")
	}
	return legacyCredentialPlan{
		SourcePath: sourcePath, Profile: selection.name, AppID: account.AppID, Destination: request.AppSecretFile, Inline: selection.singleAccount,
		canonicalSource: canonical, sourceRevision: configRevision(source), draftRevision: configRevision(candidate),
	}, nil
}

func legacyLiteralAppSecret(app legacyImportObject) (string, error) {
	var secret string
	invalid := errors.New("旧单账号配置未保存可直接复制的 App Secret，请为候选配置指定已有私有凭据文件")
	if json.Unmarshal(app["secret"], &secret) != nil {
		return "", invalid
	}
	secret = strings.TrimSpace(secret)
	if secret == "" || len(secret) >= 64<<10 || strings.ContainsAny(secret, "\x00\r\n") || strings.Contains(secret, "${") {
		return "", invalid
	}
	return secret, nil
}

func (plan legacyCredentialPlan) copy(ctx context.Context, provider string, candidate []byte) error {
	if plan.draftRevision != configRevision(candidate) {
		return errors.New("候选配置已变化，请重新预览凭据复制")
	}
	canonical, err := filepath.EvalSymlinks(plan.SourcePath)
	if err != nil || canonical != plan.canonicalSource {
		return errors.New("旧配置位置已变化，请重新预览凭据复制")
	}
	source, err := bridge.ReadRegularConfigJSON(canonical)
	if err != nil || configRevision(source) != plan.sourceRevision {
		return errors.New("旧配置内容已变化或无法读取，请重新预览凭据复制")
	}
	if plan.Inline {
		return plan.copyInline(ctx, source)
	}

	// The provider returns one secret for the selected profile and App.
	id := "app-" + plan.AppID
	request, err := json.Marshal(struct {
		ProtocolVersion int      `json:"protocolVersion"`
		Provider        string   `json:"provider"`
		IDs             []string `json:"ids"`
	}{1, "lark-channel", []string{id}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd, err := legacyCredentialCommand(ctx, provider)
	if err != nil {
		return err
	}
	cmd.Env = append(os.Environ(), "LARK_CHANNEL_HOME="+filepath.Dir(canonical), "LARK_CHANNEL_PROFILE="+plan.Profile)
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stderr = io.Discard
	var output legacySecretResponse
	cmd.Stdout, cmd.WaitDelay = &output, time.Second
	if err := cmd.Run(); err != nil {
		return errors.New("旧桥未能读取所选应用的 App Secret")
	}
	var response struct {
		ProtocolVersion int                        `json:"protocolVersion"`
		Values          map[string]string          `json:"values"`
		Errors          map[string]json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(output.data.Bytes(), &response) != nil || response.ProtocolVersion != 1 ||
		len(response.Values) != 1 || len(response.Errors) != 0 {
		return errors.New("旧桥未返回所选应用的 App Secret")
	}
	secret := strings.TrimSpace(response.Values[id])
	if secret == "" || strings.ContainsAny(secret, "\x00\r\n") {
		return errors.New("旧桥返回的 App Secret 格式无效")
	}
	if ctx.Err() != nil {
		return errors.New("已取消或超时，未写入凭据")
	}
	return writeAppSecret(plan.Destination, secret)
}

func (plan legacyCredentialPlan) copyInline(ctx context.Context, raw []byte) error {
	source, err := selectLegacyImportSource(raw, "")
	if err != nil || !source.singleAccount {
		return errors.New("旧配置已变化，请重新预览凭据复制")
	}
	app, err := source.app()
	if err != nil {
		return err
	}
	secret, err := legacyLiteralAppSecret(app)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errors.New("已取消或超时，未写入凭据")
	}
	return writeAppSecret(plan.Destination, secret)
}

type legacySecretResponse struct{ data bytes.Buffer }

func (b *legacySecretResponse) Write(data []byte) (int, error) {
	if len(data) > (64<<10)-b.data.Len() {
		return 0, errors.New("legacy credential response exceeds 64 KiB")
	}
	return b.data.Write(data)
}
