package main

import (
	"crypto/rand"
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"time"
)

//go:embed config_profile_bundle_web.html
var profileBundleImportHTML string

type profileBundleWebReview struct {
	Plan   profileBundleImportPlan
	Token  string
	expiry time.Time
}

type profileBundleWebPage struct {
	Review  *profileBundleWebReview
	Error   string
	Created string
	Name    string
	Source  string
	Patch   string
}

func profileBundleImportHandler(profiles profileRegistry) http.HandlerFunc {
	page := template.Must(newWorkspacePage("profile bundle import").Parse(profileBundleImportHTML))
	var mu sync.Mutex
	var pending *profileBundleWebReview
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Referrer-Policy", "same-origin")
		view, status := profileBundleWebPage{}, http.StatusOK
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
			if r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "无法读取操作参数，请重新预览", http.StatusBadRequest)
				return
			}
			form := r.PostForm
			action, name, source := form.Get("action"), form.Get("name"), form.Get("source")
			patch := form.Get("patch")
			view.Name, view.Source, view.Patch = name, source, patch
			var review *profileBundleWebReview
			if action == "confirm" || action == "cancel" {
				// Consume a matching ticket even when the submitted target or confirmation is invalid.
				mu.Lock()
				if pending != nil && pending.Token == form.Get("review") {
					review, pending = pending, nil
				}
				mu.Unlock()
			}
			switch {
			case !validProfileBundleImportForm(form):
				status, view.Error = http.StatusBadRequest, "操作参数无效，请重新预览。"
			case action == "preview":
				if !filepath.IsAbs(source) {
					status, view.Error = http.StatusBadRequest, "请输入本机私有 JSON 导出包的绝对路径。"
					break
				}
				if patch != "" && !filepath.IsAbs(patch) {
					status, view.Error = http.StatusBadRequest, "请输入本机 JSON 调整文件的绝对路径。"
					break
				}
				plan, err := profiles.previewBundleImport(name, source, patch)
				if err != nil {
					status, view.Error = http.StatusConflict, "无法预览私有包。请核对新配置名称，以及导出包和调整文件的路径、权限及格式。"
					break
				}
				view.Review = &profileBundleWebReview{Plan: plan, Token: rand.Text(), expiry: time.Now().Add(5 * time.Minute)}
				mu.Lock()
				pending = view.Review
				mu.Unlock()
			case review == nil || !time.Now().Before(review.expiry) || review.Plan.Name != name ||
				review.Plan.Source != source || review.Plan.Patch != patch:
				status, view.Error = http.StatusConflict, "预览已失效，请重新预览。"
			case action == "cancel":
				http.Redirect(w, r, "/profiles", http.StatusSeeOther)
				return
			default:
				plan := review.Plan
				_, err := profiles.importReviewedBundle(plan.Name, plan.Source, plan.Patch, plan.Revision)
				switch {
				case errors.Is(err, errProfileBundleChanged):
					status, view.Error = http.StatusConflict, errProfileBundleChanged.Error()
				case err != nil:
					status, view.Error = http.StatusConflict, "无法确认导入已完成。请核对导出包、调整文件和目标配置，再重新预览。"
				default:
					view.Created = plan.Name
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, view)
	}
}

func validProfileBundleImportForm(form url.Values) bool {
	fields := []string{"name", "source", "action"}
	switch form.Get("action") {
	case "preview":
	case "confirm":
		if form.Get("confirm") != "yes" {
			return false
		}
		fields = append(fields, "review", "confirm")
	case "cancel":
		fields = append(fields, "review")
	default:
		return false
	}
	if form.Has("patch") {
		fields = append(fields, "patch")
	}
	if len(form) != len(fields) {
		return false
	}
	for _, field := range fields {
		if len(form[field]) != 1 || (field != "patch" && form.Get(field) == "") {
			return false
		}
	}
	return true
}
