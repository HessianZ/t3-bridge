package main

import (
	"errors"
	"flag"
	"io"
	"path/filepath"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type manualReplacementFields struct {
	ReplaceAppID, AppID, AppSecretFile, User, Tenant string
}

func (fields manualReplacementFields) apply(raw []byte, sourcePath string) ([]byte, error) {
	return fields.prepare(raw, sourcePath, "")
}

func (fields manualReplacementFields) applyWithSecret(raw []byte, sourcePath, secret string) ([]byte, error) {
	if fields.AppSecretFile != "" {
		return raw, errors.New("请只填写新密钥或已有密钥文件路径中的一项")
	}
	value, valid := appCredentialText(secret)
	if !valid {
		return raw, errors.New("新应用密钥须为非空单行文本，且小于 64 KiB")
	}
	return fields.prepare(raw, sourcePath, value)
}

func (fields manualReplacementFields) prepare(raw []byte, sourcePath, secret string) ([]byte, error) {
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return raw, err
	}
	profiles, err := localProfiles()
	if err != nil {
		return raw, err
	}
	if secret != "" {
		fields.AppSecretFile = filepath.Join(profiles.root, "credentials", fields.AppID+".secret")
	}
	candidate, err := replaceAccountDraft(raw, config, fields.ReplaceAppID, bridge.Config{
		AppID: fields.AppID, AppSecretFile: fields.AppSecretFile, Tenant: fields.Tenant,
		StateDir: replacementStateDir(profiles.root, fields.AppID), Users: []string{fields.User},
	})
	if err != nil {
		return raw, err
	}
	if sourcePath != "" {
		sourcePath, err = filepath.Abs(sourcePath)
		if err != nil {
			return raw, errors.New("无法定位所选配置")
		}
	}
	accounts := config.Accounts()
	resolved := credentialPath(fields.AppSecretFile)
	if resolved == "" {
		return raw, errors.New("无法定位新应用的私有密钥文件")
	}
	refs := []string{resolved}
	for _, account := range accounts {
		refs = append(refs, credentialPath(account.AppSecretFile))
	}
	if credentialPathShared(refs, configCredentialProtectedPaths(sourcePath, accounts), 0) {
		return raw, errors.New("请选择独立的新应用密钥文件。当前配置引用的凭据或状态文件不能用于换绑")
	}
	if secret != "" {
		if err := writeAppSecret(fields.AppSecretFile, secret); err != nil {
			return raw, err
		}
		return candidate, nil
	}
	credential, err := readAppCredential(resolved)
	if err != nil {
		return raw, errors.New("新应用密钥文件不存在或不是可读取的私有文件")
	}
	if _, valid := appCredentialText(string(credential)); !valid {
		return raw, errors.New("新应用密钥文件需要包含单行 App Secret")
	}
	return candidate, nil
}

func runConfigReplaceApp(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config replace-app", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	draft := flags.Bool("draft-stdin", false, "read the configuration draft from stdin")
	enterSecret := flags.Bool("enter-secret", false, "read the new App Secret from a masked terminal prompt or stdin, without overwriting any file")
	var fields manualReplacementFields
	flags.StringVar(&fields.ReplaceAppID, "app-id", "", "complete App ID to replace")
	flags.StringVar(&fields.AppID, "new-app-id", "", "different App ID for the replacement")
	flags.StringVar(&fields.Tenant, "tenant", "", "tenant of the new App (feishu or lark)")
	flags.StringVar(&fields.AppSecretFile, "app-secret-file", "", "absolute path to an existing private App Secret file")
	flags.StringVar(&fields.User, "user-id", "", "initial user Open ID from the new App")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if *enterSecret && (*draft || fields.AppSecretFile != "") {
		return errors.New("-enter-secret requires a file or profile source and cannot be combined with -app-secret-file")
	}
	if *enterSecret && input == nil {
		return errors.New("-enter-secret requires App Secret input")
	}
	var raw []byte
	var sourcePath string
	var err error
	if *draft {
		raw, err = readConfigDraft(input, *path, *profile)
	} else {
		sourcePath, err = resolveConfigPath(*path, *profile, defaultConfigPath)
		if err != nil {
			return err
		}
		raw, err = bridge.ReadConfigJSON(sourcePath)
	}
	if err != nil {
		return err
	}
	var candidate []byte
	if *enterSecret {
		secret, readErr := readCredentialSetInput(input)
		if readErr != nil {
			return readErr
		}
		current, readErr := bridge.ReadConfigJSON(sourcePath)
		if readErr != nil || configRevision(raw) != configRevision(current) {
			return errConfigRevisionConflict
		}
		candidate, err = fields.applyWithSecret(raw, sourcePath, secret)
	} else {
		candidate, err = fields.apply(raw, sourcePath)
	}
	if err != nil {
		return err
	}
	_, err = output.Write(candidate)
	return err
}
