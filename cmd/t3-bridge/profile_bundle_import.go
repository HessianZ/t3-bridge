package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type profileBundleImportPlan struct {
	Name, Source, Profile, Destination, Revision string
	Patch                                        string `json:",omitempty"`
	Accounts                                     []string
	PatchedFields                                []string `json:",omitempty"`
}

type profileBundleImport struct {
	plan      profileBundleImportPlan
	candidate []byte
	secrets   map[string]string
}

var errProfileBundleChanged = errors.New("导出包已变化或调整文件已变化，请重新预览")

func readProfileBundle(path string) (profileBundle, string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return profileBundle{}, "", errors.New("请选择私有 JSON 导出包")
	}
	file, err := localfile.OpenPrivate(path)
	if err != nil {
		return profileBundle{}, "", errors.New("无法打开私有导出包")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxProfileBundleBytes+1))
	if err != nil || len(raw) > maxProfileBundleBytes || !utf8.Valid(raw) {
		return profileBundle{}, "", errors.New("导出包不可读取或大于 16 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle profileBundle
	if decoder.Decode(&bundle) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF ||
		bundle.SchemaVersion != 1 || !serviceInstanceName.MatchString(bundle.Profile) {
		return profileBundle{}, "", errors.New("导出包格式或版本无效")
	}
	return bundle, configRevision(raw), nil
}

func (r profileRegistry) prepareBundleImport(name, file, patch, expectedRevision string) (profileBundleImport, error) {
	path, err := r.file(name)
	if err != nil {
		return profileBundleImport{}, err
	}
	profileDir := filepath.Dir(path)
	if _, err := os.Lstat(profileDir); !errors.Is(err, os.ErrNotExist) {
		return profileBundleImport{}, errors.New("目标命名配置已存在或无法检查")
	}
	source, err := filepath.Abs(file)
	if err != nil {
		return profileBundleImport{}, errors.New("无法定位私有导出包")
	}
	bundle, revision, err := readProfileBundle(source)
	if err != nil {
		return profileBundleImport{}, err
	}
	plan := profileBundleImportPlan{Name: name, Source: source, Profile: bundle.Profile, Destination: path, Revision: revision}
	changes, err := readProfileBundlePatch(patch, &plan)
	if err != nil {
		return profileBundleImport{}, err
	}
	if expectedRevision != "" && plan.Revision != expectedRevision {
		return profileBundleImport{}, errProfileBundleChanged
	}
	var configJSON bytes.Buffer
	if err := json.Compact(&configJSON, bundle.Config); err != nil {
		return profileBundleImport{}, errors.New("导出包中的配置无效")
	}
	// Read account identities before host-local path validation. The shared editor
	// strictly checks the original shape and validates the patched target below.
	var cfg bridge.Config
	if configJSON.Len() > bridge.MaxConfigBytes || json.Unmarshal(configJSON.Bytes(), &cfg) != nil {
		return profileBundleImport{}, errors.New("导出包中的配置无效")
	}
	accounts := cfg.Accounts()
	secrets := make(map[string]string, len(accounts))
	for _, entry := range bundle.AppSecrets {
		value, ok := appCredentialText(entry.Secret)
		if entry.AppID == "" || !ok || secrets[entry.AppID] != "" {
			return profileBundleImport{}, errors.New("导出包含无效或重复的 App 凭据")
		}
		secrets[entry.AppID] = value
	}
	if len(secrets) != len(accounts) {
		return profileBundleImport{}, errors.New("导出包中的凭据与配置 App 不一致")
	}
	credentialFiles := make(map[string]string, len(accounts))
	for i, account := range accounts {
		if secrets[account.AppID] == "" {
			return profileBundleImport{}, errors.New("导出包中的凭据与配置 App 不一致")
		}
		credentialFiles[account.AppID] = profileCredentialPath(profileDir, i)
		plan.Accounts = append(plan.Accounts, account.AppID)
	}
	candidate, err := r.profileCandidate(name, configJSON.Bytes(), cfg, credentialFiles, changes)
	if err != nil {
		return profileBundleImport{}, fmt.Errorf("导出包无法生成目标配置：%w", err)
	}
	return profileBundleImport{plan: plan, candidate: candidate, secrets: secrets}, nil
}

func readProfileBundlePatch(patch string, plan *profileBundleImportPlan) ([]json.RawMessage, error) {
	if patch == "" {
		return nil, nil
	}
	path, err := filepath.Abs(patch)
	if err != nil {
		return nil, errors.New("无法定位迁移调整文件")
	}
	raw, err := bridge.ReadRegularConfigJSON(path)
	if err != nil {
		return nil, errors.New("无法读取本机 JSON 调整文件")
	}
	var changes []json.RawMessage
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) || json.Unmarshal(raw, &changes) != nil {
		return nil, errors.New("迁移调整须使用 config patch 的 JSON 数组格式")
	}
	for _, change := range changes {
		var edit struct{ Path []string }
		if json.Unmarshal(change, &edit) != nil || len(edit.Path) == 0 {
			return nil, errors.New("迁移调整须指定字段路径")
		}
		fields := edit.Path
		if fields[0] == "additionalAccounts" {
			if len(fields) < 3 {
				return nil, errors.New("迁移调整须选择已有附加 App 的字段")
			}
			fields = fields[2:]
		}
		switch fields[0] {
		case "appId", "tenant", "appSecretFile", "stateDir", "receiverMachine", "receiverAuthorized", "additionalAccounts":
			return nil, errors.New("迁移调整不能修改 App 身份或导入管理的字段")
		}
		plan.PatchedFields = append(plan.PatchedFields, strings.Join(edit.Path, "."))
	}
	plan.Patch = path
	plan.Revision = configRevision([]byte(plan.Revision + ":" + configRevision(raw)))
	return changes, nil
}

func (r profileRegistry) previewBundleImport(name, source, patch string) (profileBundleImportPlan, error) {
	prepared, err := r.prepareBundleImport(name, source, patch, "")
	return prepared.plan, err
}

func (r profileRegistry) importReviewedBundle(name, source, patch, revision string) (string, error) {
	if revision == "" {
		return "", errors.New("请先预览私有导出包")
	}
	return r.importBundle(name, source, patch, revision)
}

func (r profileRegistry) importBundle(name, source, patch, revision string) (string, error) {
	prepared, err := r.prepareBundleImport(name, source, patch, revision)
	if err != nil {
		return "", err
	}
	path := prepared.plan.Destination
	profileDir := filepath.Dir(path)

	if err := ensureConfigParent(r.root); err != nil {
		return "", err
	}
	lock, err := lockConfigFile(filepath.Join(r.root, "active-profile"))
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if _, err := os.Lstat(profileDir); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("目标命名配置已存在或无法检查")
	}
	parent := filepath.Dir(profileDir)
	staging := filepath.Join(parent, ".profile-import-"+strings.ToLower(rand.Text()))
	if err := localfile.MkdirAllPrivate(staging); err != nil {
		return "", errors.New("无法创建临时私有配置目录")
	}
	defer os.RemoveAll(staging)
	for i, appID := range prepared.plan.Accounts {
		if err := writeAppSecret(profileCredentialPath(staging, i), prepared.secrets[appID]); err != nil {
			return "", err
		}
	}
	if _, err := localfile.WritePrivateAtomic(filepath.Join(staging, "config.json"), prepared.candidate, true); err != nil {
		return "", errors.New("无法写入导入配置")
	}
	if err := os.Rename(staging, profileDir); err != nil {
		return "", errors.New("无法发布导入的命名配置")
	}
	return path, nil
}

func profileCredentialPath(dir string, index int) string {
	return filepath.Join(dir, "credentials", "app-"+strconv.Itoa(index)+".secret")
}

func (r profileRegistry) runBundleImport(name string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge profile import NAME", flag.ContinueOnError)
	flags.SetOutput(output)
	source := flags.String("from", "", "read a private profile bundle from this JSON file")
	patch := flags.String("patch", "", "apply host-specific config edits from a local JSON array file")
	preview := flags.Bool("preview", false, "validate and describe the import without writing")
	revision := flags.String("expected-revision", "", "import only this version from a previous preview")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *source == "" || flags.NArg() != 0 {
		return errors.New("profile import NAME requires -from BUNDLE")
	}
	if *preview {
		if *revision != "" {
			return errors.New("choose preview or import with an expected revision")
		}
		plan, err := r.previewBundleImport(name, *source, *patch)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	}
	path, err := r.importBundle(name, *source, *patch, *revision)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, path)
	return err
}
