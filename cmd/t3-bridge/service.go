package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const serviceUsage = "usage: t3-bridge service list | t3-bridge service {render|install|status|start|stop|restart|unregister|logs} -instance NAME [options]"

var serviceInstanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

type serviceInstance struct {
	platform string
	name     string
	id       string
	file     string
	logDir   string
	domain   string
}

func runService(args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
		_, err := fmt.Fprintln(output, serviceUsage)
		return err
	}
	if len(args) == 0 {
		return errors.New(serviceUsage)
	}
	if args[0] == "list" || args[0] == "logs" {
		return runServiceInventory(args, output)
	}
	action := args[0]
	switch action {
	case "render", "install", "status", "start", "stop", "restart", "unregister":
	default:
		return errors.New(serviceUsage)
	}
	flags := flag.NewFlagSet("t3-bridge service "+action, flag.ContinueOnError)
	flags.SetOutput(output)
	name := flags.String("instance", "", "instance name (lowercase letters, digits and hyphens)")
	platform, binary, config, profile := runtime.GOOS, "", "", ""
	if action == "render" {
		flags.StringVar(&platform, "platform", platform, "target platform (darwin or linux)")
	}
	if action == "render" || action == "install" {
		flags.StringVar(&binary, "binary", "", "path to the bridge executable (defaults to this executable)")
		flags.StringVar(&config, "config", config, "path to the private JSON configuration")
		flags.StringVar(&profile, "profile", profile, "use the named configuration profile")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(serviceUsage)
	}
	if !serviceInstanceName.MatchString(*name) {
		return errors.New("-instance must contain 1–48 lowercase letters, digits or hyphens and begin with a letter or digit")
	}
	if platform != "darwin" && platform != "linux" {
		return errors.New("service management supports macOS launchd and Linux systemd user services")
	}
	if action != "render" && os.Geteuid() <= 0 {
		return errors.New("run service management as the user who will run the bridge, without sudo")
	}
	instance, err := resolveServiceInstance(platform, *name, action == "render")
	if err != nil {
		return err
	}
	if action != "install" && action != "render" {
		return instance.manage(action, runServiceManager, output)
	}
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return err
		}
	}
	binary, err = serviceAbsolutePath(binary)
	if err != nil {
		return fmt.Errorf("-binary: %w", err)
	}
	config, err = resolveConfigPath(config, profile, defaultConfigPath)
	if err != nil {
		return err
	}
	config, err = serviceAbsolutePath(config)
	if err != nil {
		return fmt.Errorf("-config: %w", err)
	}
	definition := instance.render(binary, config)
	if action == "render" {
		_, err := io.WriteString(output, definition)
		return err
	}
	if err := validateServiceInputs(binary, config); err != nil {
		return err
	}
	if err := instance.install(definition, runServiceManager); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s installed at %s\n", instance.id, instance.file)
	return err
}

func resolveServiceInstance(platform, name string, render bool) (serviceInstance, error) {
	instance := serviceInstance{platform: platform, name: name}
	if platform == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return serviceInstance{}, err
		}
		home, err = serviceAbsolutePath(home)
		if err != nil {
			return serviceInstance{}, err
		}
		instance.id = "ai.t3-bridge.managed." + name
		instance.file = filepath.Join(home, "Library", "LaunchAgents", instance.id+".plist")
		instance.logDir = filepath.Join(home, "Library", "Logs", "t3-bridge", name)
		instance.domain = fmt.Sprintf("gui/%d", os.Geteuid())
	} else {
		instance.id = "t3-bridge-managed-" + name + ".service"
		if !render {
			configDir, err := os.UserConfigDir()
			if err != nil {
				return serviceInstance{}, err
			}
			instance.file = filepath.Join(configDir, "systemd", "user", instance.id)
		}
	}
	return instance, nil
}

func serviceAbsolutePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(absolute) || strings.ContainsFunc(absolute, func(r rune) bool {
		return unicode.IsControl(r) || r == '\ufffe' || r == '\uffff'
	}) {
		return "", errors.New("invalid path")
	}
	return absolute, nil
}

func validateServiceInputs(binary, config string) error {
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("-binary must point to an executable file")
	}
	info, err = os.Stat(config)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("-config must point to a private regular file (such as mode 0600)")
	}
	_, err = bridge.LoadConfig(config)
	return err
}
