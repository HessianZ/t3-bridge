package main

import (
	"bytes"
	_ "embed"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed config_profiles.html
var profilePageHTML string

func registerProfileWebRoutes(mux *http.ServeMux, profiles profileRegistry) {
	page := newWorkspacePage("profiles").Parse(profilePageHTML)
	mux.HandleFunc("GET /profiles", func(w http.ResponseWriter, r *http.Request) {
		active, activeErr := profiles.active()
		names, namesErr := profiles.names()
		archives, archiveErr := os.ReadDir(filepath.Join(profiles.root, "profile-archive"))
		if errors.Is(archiveErr, os.ErrNotExist) {
			archiveErr = nil
		}
		if errors.Join(activeErr, namesErr, archiveErr) != nil {
			http.Error(w, "无法读取命名配置列表", http.StatusConflict)
			return
		}
		data := struct {
			Active   string
			Names    []string
			Archives []struct{ ID, Name string }
		}{Active: active, Names: names}
		for _, entry := range archives {
			if name, ok := archivedProfileName(entry.Name()); ok && entry.IsDir() {
				data.Archives = append(data.Archives, struct{ ID, Name string }{entry.Name(), name})
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Native same-origin form POSTs need a non-opaque Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		_ = page.Execute(w, r, data)
	})
	lifecycle := profileLifecycleHandler(profiles)
	mux.HandleFunc("GET /profiles/lifecycle", lifecycle)
	mux.HandleFunc("POST /profiles/lifecycle", lifecycle)
	bundleImport := profileBundleImportHandler(profiles)
	mux.HandleFunc("GET /profiles/import", bundleImport)
	mux.HandleFunc("POST /profiles/import", bundleImport)
	mux.HandleFunc("POST /profiles", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "无法读取操作参数", http.StatusBadRequest)
			return
		}
		name := r.PostForm.Get("name")
		action := r.PostForm.Get("action")
		args := []string{action, name}
		redirect := "/profiles"
		switch action {
		case "create":
			if source := r.PostForm.Get("from"); source != "" {
				path, err := profiles.existingFile(source)
				if err != nil {
					http.Error(w, err.Error(), http.StatusConflict)
					return
				}
				args = append(args, "-from", path)
			}
			redirect = "/?source=profile-" + name
		case "use":
			redirect = "/?source=profile-" + name
		case "archive":
		case "restore":
			if _, ok := archivedProfileName(name); !ok {
				http.Error(w, "请选择已有的归档", http.StatusBadRequest)
				return
			}
			args[1] = filepath.Join(profiles.root, "profile-archive", name, "config.json")
		default:
			http.Error(w, "不支持的命名配置操作", http.StatusBadRequest)
			return
		}
		if err := profiles.run(args, io.Discard); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
	})
	mux.HandleFunc("GET /profiles/export", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		var out bytes.Buffer
		if err := profiles.run([]string{"export", name}, &out); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
		_, _ = out.WriteTo(w)
	})
}
