package main

import (
	_ "embed"
	"html/template"
)

//go:embed config_workspace.css
var workspaceCSS string

//go:embed config_workspace_header.tmpl
var workspaceHeader string

//go:embed t3-bridge-icon.png
var workspaceIcon []byte

type workspaceNavigation struct {
	Section string
	Source  string
}

func newWorkspacePage(name string) *template.Template {
	return template.Must(template.New(name).Funcs(template.FuncMap{
		"workspaceNav": func(section, source string) workspaceNavigation {
			return workspaceNavigation{Section: section, Source: source}
		},
	}).Parse(workspaceHeader))
}
