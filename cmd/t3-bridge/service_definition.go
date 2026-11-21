package main

import (
	"fmt"
	"html"
	"strings"
)

const serviceMarker = "Managed by t3-bridge service v1: "

func (s serviceInstance) header() string {
	if s.platform == "darwin" {
		// Instance names can contain consecutive hyphens, which XML comments forbid.
		owner := strings.ReplaceAll(s.id, "-", "_")
		return `<?xml version="1.0" encoding="UTF-8"?>` + "\n<!-- " + serviceMarker + owner + " -->\n"
	}
	return "# " + serviceMarker + s.id + "\n"
}

func (s serviceInstance) render(binary, config string) string {
	if s.platform == "darwin" {
		return s.header() + fmt.Sprintf(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>run</string>
        <string>-config</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>ExitTimeOut</key>
    <integer>30</integer>
    <key>Umask</key>
    <integer>63</integer>
    <key>StandardOutPath</key>
    <string>%s/stdout.log</string>
    <key>StandardErrorPath</key>
    <string>%s/stderr.log</string>
</dict>
</plist>
`, s.id, html.EscapeString(binary), html.EscapeString(config), html.EscapeString(s.logDir), html.EscapeString(s.logDir))
	}
	// The leading colon disables environment expansion. Percent specifiers still need escaping.
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return s.header() + fmt.Sprintf(`[Unit]
Description=T3 Bridge for Feishu (%s)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=":%s" -config "%s"
Restart=on-failure
RestartSec=10
TimeoutStopSec=30
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
`, s.name, escape.Replace(binary), escape.Replace(config))
}
