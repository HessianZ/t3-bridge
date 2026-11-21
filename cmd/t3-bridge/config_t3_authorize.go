package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func runConfigAuthorizeT3(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config authorize-t3", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	draftInput := flags.Bool("draft-stdin", false, "read the configuration draft from stdin")
	appID := flags.String("app-id", "", "select a configured app (defaults to the primary app)")
	tokenPath := flags.String("token-file", "", "private absolute JSON credential path (defaults to mcp.tokenFile)")
	replace := flags.Bool("replace", false, "replace the existing private credential at the selected path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	account, err := loadT3ConfigAccount(input, *path, *profile, *appID, *draftInput)
	if err != nil {
		return err
	}
	config := account.MCP
	if *tokenPath != "" {
		config.TokenFile = *tokenPath
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 5*time.Minute)
	defer cancel()
	if err := authorizeConfiguredT3(ctx, config, *replace, func(address string) error {
		_, err := fmt.Fprintf(output, "Open this URL on this computer to authorize T3 Bridge:\n%s\n\n"+
			"If T3 asks for a Sign-in code, open Settings → Sharing & access → Use T3 Code in another app (MCP) in the same T3 environment.\n"+
			"Paste that one-time code into the T3 page. Get a new code if it has expired or already been used.\n"+
			"Choose Supervised for an approval-required route. Bridge waits up to 5 minutes.\n", address)
		return err
	}); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("T3 approval wait expired; run config authorize-t3 again to open a new authorization page: %w", err)
		}
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("T3 authorization canceled: %w", err)
		}
		return err
	}
	_, err = fmt.Fprintf(output, "T3 OAuth credential saved at %s\n", config.TokenFile)
	return err
}

func authorizeConfiguredT3(ctx context.Context, config bridge.MCPConfig, replace bool, showURL func(string) error) error {
	path := config.TokenFile
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".json") {
		return errors.New("T3 OAuth token file must be an absolute .json path")
	}
	baseline, err := t3TokenRevision(path)
	if err != nil {
		return err
	}
	if baseline != "" && !replace {
		return errors.New("T3 OAuth token file already exists; use -replace to reauthorize or choose another path")
	}
	credential, err := bridge.AuthorizeT3(ctx, config, showURL)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return err
	}
	if err := localfile.MkdirAllPrivate(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare private T3 credential directory: %w", err)
	}
	lock, err := localfile.Lock(path+".lock", false)
	if err != nil {
		return fmt.Errorf("lock T3 credential: %w", err)
	}
	defer lock.Close()
	current, err := t3TokenRevision(path)
	if err != nil {
		return err
	}
	if current != baseline {
		return errors.New("T3 OAuth token file changed during authorization")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	published, err := localfile.WritePrivateAtomic(path, credential, baseline == "")
	if err != nil && published {
		return fmt.Errorf("T3 credential was published but directory sync failed: %w", err)
	}
	return err
}

func t3TokenRevision(path string) (string, error) {
	file, err := localfile.OpenPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("existing T3 OAuth token file must be private")
	}
	defer file.Close()
	const maxTokenBytes = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(file, maxTokenBytes+1))
	if err != nil || len(raw) > maxTokenBytes {
		return "", errors.New("cannot read the existing T3 OAuth token file")
	}
	return configRevision(raw), nil
}
