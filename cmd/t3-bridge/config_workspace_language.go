package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/localfile"
)

type workspaceLanguage string

const (
	workspaceAuto workspaceLanguage = "auto"
	workspaceEN   workspaceLanguage = "en"
	workspaceZH   workspaceLanguage = "zh-CN"
)

type workspaceLanguageKey struct{}

//go:embed config_workspace_en.json
var workspaceEnglishJSON []byte

var workspaceEnglish = func() map[string]string {
	var messages map[string]string
	if err := json.Unmarshal(workspaceEnglishJSON, &messages); err != nil {
		panic(err)
	}
	return messages
}()

func (lang workspaceLanguage) text(source string) string {
	if lang == workspaceEN {
		if translated := workspaceEnglish[source]; translated != "" {
			return translated
		}
	}
	return source
}

func (lang workspaceLanguage) sourceLabel(label string) string {
	for _, prefix := range []string{"配置文件 · ", "命名配置 · "} {
		if name, ok := strings.CutPrefix(label, prefix); ok {
			return lang.text(prefix) + name
		}
	}
	return lang.text(label)
}

func localeLanguage(tag string) workspaceLanguage {
	base, _, _ := strings.Cut(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-")), "-")
	switch strings.SplitN(base, ".", 2)[0] {
	case "zh":
		return workspaceZH
	case "en":
		return workspaceEN
	default:
		return workspaceAuto
	}
}

func systemWorkspaceLanguage() workspaceLanguage {
	if native := platformWorkspaceLanguage(); native != workspaceAuto {
		return native
	}
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if value := os.Getenv(key); value != "" {
			for tag := range strings.SplitSeq(value, ":") {
				if lang := localeLanguage(tag); lang != workspaceAuto {
					return lang
				}
			}
			return workspaceEN
		}
	}
	return workspaceEN
}

func browserWorkspaceLanguage(header string, fallback workspaceLanguage) workspaceLanguage {
	best := 0.0
	for preference := range strings.SplitSeq(header, ",") {
		tag, params, _ := strings.Cut(preference, ";")
		lang := localeLanguage(tag)
		if lang == workspaceAuto {
			continue
		}
		weight := 1.0
		if name, value, found := strings.Cut(strings.TrimSpace(params), "="); found && name == "q" {
			weight, _ = strconv.ParseFloat(value, 64)
		}
		if weight > best && weight <= 1 {
			best, fallback = weight, lang
		}
	}
	return fallback
}

func (p profileRegistry) workspaceLanguage() workspaceLanguage {
	if p.root != "" {
		f, err := localfile.OpenPrivate(filepath.Join(p.root, "workspace-language"))
		if err == nil {
			defer f.Close()
			value, _ := io.ReadAll(io.LimitReader(f, 16))
			switch lang := workspaceLanguage(strings.TrimSpace(string(value))); lang {
			case workspaceEN, workspaceZH:
				return lang
			}
		}
	}
	return workspaceAuto
}

func (p profileRegistry) saveWorkspaceLanguage(lang workspaceLanguage) error {
	if err := localfile.MkdirAllPrivate(p.root); err != nil {
		return err
	}
	_, err := localfile.WritePrivateAtomic(filepath.Join(p.root, "workspace-language"), []byte(lang+"\n"), false)
	return err
}

func requestWorkspaceLanguage(r *http.Request) workspaceLanguage {
	if lang, ok := r.Context().Value(workspaceLanguageKey{}).(workspaceLanguage); ok {
		return lang
	}
	// The individual page handlers can also be used without a workspace server.
	return browserWorkspaceLanguage(r.Header.Get("Accept-Language"), workspaceZH)
}

func (p profileRegistry) withWorkspaceLanguage(handler http.Handler, desktop bool) http.Handler {
	system := systemWorkspaceLanguage()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := p.workspaceLanguage()
		if lang == workspaceAuto {
			lang = system
			if accept := r.Header.Get("Accept-Language"); !desktop && accept != "" {
				lang = browserWorkspaceLanguage(accept, workspaceEN)
			}
		}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), workspaceLanguageKey{}, lang)))
	})
}

func (p profileRegistry) workspaceLanguageHandler(w http.ResponseWriter, r *http.Request) {
	if !runtimeRequestOriginAllowed(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		if target := r.URL.Query().Get("return"); target != "" {
			destination, err := url.Parse(target)
			if err != nil || destination.Host != "" || destination.IsAbs() ||
				!strings.HasPrefix(destination.Path, "/") || strings.HasPrefix(destination.Path, "//") ||
				strings.ContainsAny(destination.Path, "\\\r\n") {
				http.Error(w, "invalid return URL", http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, destination.String(), http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, string(p.workspaceLanguage()))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid language", http.StatusBadRequest)
		return
	}
	values := r.PostForm["language"]
	if len(values) != 1 {
		http.Error(w, "invalid language", http.StatusBadRequest)
		return
	}
	lang := workspaceLanguage(values[0])
	switch lang {
	case workspaceAuto, workspaceEN, workspaceZH:
	default:
		http.Error(w, "invalid language", http.StatusBadRequest)
		return
	}
	if p.root == "" || p.saveWorkspaceLanguage(lang) != nil {
		http.Error(w, "could not save language", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
