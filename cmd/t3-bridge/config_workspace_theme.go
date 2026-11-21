package main

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
)

//go:embed config_workspace.css
var workspaceCSS string

//go:embed config_workspace_header.tmpl
var workspaceHeader string

//go:embed config_workspace_language.js
var workspaceLanguageJS string

//go:embed t3-bridge-icon.png
var workspaceIcon []byte

type workspaceNavigation struct {
	Section         string
	Source          string
	LanguageControl bool
}

type workspacePage struct {
	name             string
	funcs            template.FuncMap
	english, chinese *template.Template
}

func newWorkspacePage(name string) *workspacePage {
	return &workspacePage{name: name, funcs: template.FuncMap{}}
}

func (page *workspacePage) Funcs(funcs template.FuncMap) *workspacePage {
	for name, function := range funcs {
		page.funcs[name] = function
	}
	return page
}

func (page *workspacePage) Parse(markup string) *workspacePage {
	parse := func(lang workspaceLanguage) *template.Template {
		base := template.New(page.name).Funcs(page.funcs).Funcs(template.FuncMap{
			"tr":          lang.text,
			"trf":         func(source string, args ...any) string { return fmt.Sprintf(lang.text(source), args...) },
			"sourceLabel": lang.sourceLabel,
			"uiLanguage":  func() workspaceLanguage { return lang },
			"workspaceNav": func(section, source string) workspaceNavigation {
				return workspaceNavigation{Section: section, Source: source, LanguageControl: page.name != "credentials"}
			},
		})
		return template.Must(template.Must(base.Parse(workspaceHeader)).Parse(markup))
	}
	page.english, page.chinese = parse(workspaceEN), parse(workspaceZH)
	return page
}

func (page *workspacePage) Execute(w io.Writer, r *http.Request, data any) error {
	if requestWorkspaceLanguage(r) == workspaceEN {
		return page.english.Execute(w, data)
	}
	return page.chinese.Execute(w, data)
}
