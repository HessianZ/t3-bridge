package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

func (s serviceInstance) followTelemetry(ctx context.Context, lines int, manager serviceManager, journal func(context.Context, io.Writer, receiverProcessAccount, ...string) error, output io.Writer, account receiverProcessAccount) error {
	if err := s.checkDefinition(); err != nil {
		return fmt.Errorf("service definition: %w", err)
	}
	if _, err := s.inspect(manager); err != nil {
		return err
	}
	if s.platform == "darwin" {
		return followServiceTelemetryFiles(ctx, s.logDir, lines, localfile.CheckPrivateDir, output, account)
	}
	return journal(ctx, output, account, s.journalArgs(lines, "--output=cat", "--follow")...)
}

func followServiceJournal(ctx context.Context, output io.Writer, account receiverProcessAccount, args ...string) error {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(commandCtx, "journalctl", args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	input, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("follow journal: %w", err)
	}
	streamErr := writeServiceTelemetry(input, output, account)
	if streamErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if streamErr != nil {
		return streamErr
	}
	if waitErr != nil {
		return fmt.Errorf("follow journal: %w", waitErr)
	}
	return nil
}
