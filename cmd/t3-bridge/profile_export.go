package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type profileExportPlan struct {
	Name, Source, Revision string
	Credentials            int
}

type profileExportSource struct {
	plan   profileExportPlan
	config bridge.Config
	raw    []byte
}

type profileBundle struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Profile       string               `json:"profile"`
	Config        json.RawMessage      `json:"config"`
	AppSecrets    []profileBundleEntry `json:"appSecrets"`
}

type profileBundleEntry struct {
	AppID  string `json:"appId"`
	Secret string `json:"appSecret"`
}

const maxProfileBundleBytes = 16 << 20

func (r profileRegistry) previewExport(name string) (profileExportPlan, error) {
	source, err := r.readProfileExport(name)
	return source.plan, err
}

func (r profileRegistry) readProfileExport(name string) (profileExportSource, error) {
	path, err := r.existingFile(name)
	if err != nil {
		return profileExportSource{}, err
	}
	raw, err := bridge.ReadPrivateConfigJSON(path)
	if err != nil {
		return profileExportSource{}, err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return profileExportSource{}, err
	}
	plan := profileExportPlan{Name: name, Source: path, Revision: configRevision(raw), Credentials: 1 + len(config.AdditionalAccounts)}
	return profileExportSource{plan: plan, config: config, raw: raw}, nil
}

func (r profileRegistry) exportFile(name, dest, revision string, includeSecrets, force bool) error {
	if !filepath.IsAbs(dest) || !strings.EqualFold(filepath.Ext(dest), ".json") {
		return errors.New("请选择本机 JSON 导出文件的绝对路径")
	}
	if revision == "" {
		return errors.New("请先预览当前命名配置")
	}
	path, err := r.existingFile(name)
	if err != nil {
		return err
	}
	lock, err := lockConfigFile(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	source, err := r.readProfileExport(name)
	if err != nil {
		return err
	}
	if source.plan.Revision != revision {
		return errConfigRevisionConflict
	}

	// Configuration and state paths must remain usable after export.
	if err := r.checkProfileExportDestination(dest, source, force); err != nil {
		return err
	}
	body, err := bridge.EditConfigJSON(source.raw, nil)
	if err != nil {
		return err
	}
	if includeSecrets {
		bundle := profileBundle{SchemaVersion: 1, Profile: name, Config: body}
		items, err := listAppCredentials(source.plan.Source)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.configVersion != revision {
				return errConfigRevisionConflict
			}
			secret, err := item.value()
			if err != nil {
				return fmt.Errorf("无法读取 App %q 的凭据，请先核对私有文件", item.AppID)
			}
			bundle.AppSecrets = append(bundle.AppSecrets, profileBundleEntry{AppID: item.AppID, Secret: secret})
		}
		body, err = json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		body = append(body, '\n')
		if len(body) > maxProfileBundleBytes {
			return errors.New("命名配置导出包超过 16 MiB")
		}
		if err := localfile.MkdirAllPrivate(filepath.Dir(dest)); err != nil {
			return errors.New("含凭据导出需要当前用户的私有目录")
		}
	} else if err := ensureConfigParent(filepath.Dir(dest)); err != nil {
		return err
	}
	published, err := localfile.WritePrivateAtomic(dest, body, !force)
	switch {
	case err == nil:
		return nil
	case published:
		return errors.New("文件已写入，但磁盘同步结果未确认，请读回核对")
	case errors.Is(err, os.ErrExist):
		return errors.New("导出目标已存在，请选择新的路径")
	default:
		return errors.New("无法写入本机私有导出文件")
	}
}

func (r profileRegistry) checkProfileExportDestination(dest string, source profileExportSource, force bool) error {
	resolved := credentialPath(dest)
	if resolved == "" {
		return errors.New("无法定位导出路径")
	}
	protected := []string{filepath.Join(r.root, "profiles"), filepath.Join(r.root, "profile-archive"), filepath.Join(r.root, "state"),
		filepath.Join(r.root, "config.json"), filepath.Join(r.root, "config.json.backup.json"),
		source.plan.Source, source.plan.Source + ".backup.json"}
	for _, account := range source.config.Accounts() {
		protected = append(protected, account.AppSecretFile, account.StateDir, account.MCP.TokenFile, account.MCP.RuntimeFile,
			account.Classifier.KeyFile, account.AgentReportsFile)
	}
	info, statErr := os.Lstat(dest)
	for _, target := range protected {
		canonical := credentialPath(target)
		if canonical == "" {
			continue
		}
		relative, relErr := filepath.Rel(canonical, resolved)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("导出目标与受管配置、状态或凭据路径重叠")
		}
		if statErr == nil {
			if other, err := os.Stat(canonical); err == nil && os.SameFile(info, other) {
				return errors.New("导出目标与受管配置、状态或凭据路径重叠")
			}
		}
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil || !info.Mode().IsRegular() {
		return errors.New("导出目标须为新的路径或私有普通文件")
	}
	if !force {
		return errors.New("导出目标已存在，请选择新的路径")
	}
	file, err := localfile.OpenPrivate(dest)
	if err != nil {
		return errors.New("只可覆盖私有普通文件")
	}
	return file.Close()
}

func (r profileRegistry) runExport(name string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge profile export NAME", flag.ContinueOnError)
	flags.SetOutput(output)
	dest := flags.String("output", "", "destination JSON file (required for exports containing App Secrets)")
	includeSecrets := flags.Bool("include-secrets", false, "include selected profile App Secrets in a private bundle")
	force := flags.Bool("force", false, "replace an existing private destination")
	yes := flags.Bool("yes", false, "confirm exporting App Secrets")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*force && *dest == "") {
		return errors.New(profileUsage)
	}
	if *includeSecrets && (!*yes || *dest == "") {
		return errors.New("profile export -include-secrets requires -yes and a private -output file")
	}
	source, err := r.readProfileExport(name)
	if err != nil {
		return err
	}
	if *dest != "" {
		path, err := filepath.Abs(*dest)
		if err != nil {
			return err
		}
		if err := r.exportFile(name, path, source.plan.Revision, *includeSecrets, *force); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, path)
		return err
	}
	body, err := bridge.EditConfigJSON(source.raw, nil)
	if err != nil {
		return err
	}
	_, err = output.Write(body)
	return err
}
