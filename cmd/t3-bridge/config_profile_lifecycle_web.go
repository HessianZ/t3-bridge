package main

import (
	"crypto/rand"
	_ "embed"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed config_profile_lifecycle_web.html
var profileLifecycleHTML string

type profileLifecycleTarget struct {
	Name, Operation, Destination string
}

type profileLifecycleReview struct {
	Target profileLifecycleTarget
	Token  string
	Purge  profilePurgePlan
	Export profileExportPlan
	expiry time.Time
}

type profileLifecyclePage struct {
	Name, Message, Error, ExportedPath string
	Review                             *profileLifecycleReview
}

func profileLifecycleHandler(profiles profileRegistry) http.HandlerFunc {
	page := newWorkspacePage("profile lifecycle").Parse(profileLifecycleHTML)
	var reviews profileLifecycleReviews
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.Method != http.MethodPost {
			name := r.URL.Query().Get("name")
			if !serviceInstanceName.MatchString(name) {
				http.Error(w, "请选择已有的命名配置", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = page.Execute(w, r, profileLifecyclePage{Name: name})
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		if r.URL.RawQuery != "" || r.ParseForm() != nil {
			http.Error(w, "无法读取操作参数，请重新预览", http.StatusBadRequest)
			return
		}
		form := r.PostForm
		action := form.Get("action")
		var review *profileLifecycleReview
		if action == "confirm" || action == "cancel" {
			// Consume the ticket before checking the target or attempting any write.
			review = reviews.take(form.Get("review"))
		}
		if !validProfileLifecycleForm(form) {
			http.Error(w, "操作参数无效，请重新预览", http.StatusBadRequest)
			return
		}
		target := profileLifecycleTarget{
			Name: form.Get("name"), Operation: form.Get("operation"), Destination: form.Get("destination"),
		}
		view, status := profileLifecyclePage{}, http.StatusOK
		if target.Operation == "export" {
			view.Name = target.Name
		}
		switch {
		case action == "preview":
			reviews.set(nil)
			var err error
			view.Review, err = previewProfileLifecycle(profiles, target)
			if err != nil {
				status, view.Error = http.StatusConflict, err.Error()
			} else {
				reviews.set(view.Review)
			}
		case review == nil || review.Target != target:
			status, view.Error = http.StatusConflict, "预览已失效，请重新预览。"
		case action == "cancel":
			http.Redirect(w, r, "/profiles", http.StatusSeeOther)
			return
		case target.Operation == "purge":
			if err := profiles.applyPurge(target.Name, review.Purge.Revision); err != nil {
				status, view.Error = http.StatusConflict, "无法确认永久清除已完成。请核对配置、接收端状态与本机目录，再重新预览。"
			} else {
				view.Message = "已永久清除命名配置。"
			}
		default:
			err := profiles.exportFile(target.Name, target.Destination, review.Export.Revision, true, false)
			switch {
			case errors.Is(err, errConfigRevisionConflict):
				status, view.Error = http.StatusConflict, "来源配置已变化，请重新预览。"
			case err != nil:
				status, view.Error = http.StatusConflict, "无法确认私有导出已完成。请核对配置、App Secret 文件与目标路径，再重新预览。"
			default:
				view.Name, view.Message, view.ExportedPath = "", "已写入本机私有导出文件。", target.Destination
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}

func validProfileLifecycleForm(form url.Values) bool {
	fields := []string{"name", "action", "operation"}
	if !serviceInstanceName.MatchString(form.Get("name")) {
		return false
	}
	switch form.Get("operation") {
	case "purge":
	case "export":
		fields = append(fields, "destination")
	default:
		return false
	}
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
	if len(form) != len(fields) {
		return false
	}
	for _, field := range fields {
		if len(form[field]) != 1 || form.Get(field) == "" {
			return false
		}
	}
	return true
}

func previewProfileLifecycle(profiles profileRegistry, target profileLifecycleTarget) (*profileLifecycleReview, error) {
	review := &profileLifecycleReview{Target: target, Token: rand.Text(), expiry: time.Now().Add(5 * time.Minute)}
	if target.Operation == "purge" {
		plan, err := profiles.previewPurge(target.Name)
		if err != nil {
			return nil, errors.New("无法预览永久清除。请核对所选配置及接收端状态，再重新预览。")
		}
		review.Purge = plan
		return review, nil
	}
	if !filepath.IsAbs(target.Destination) || !strings.EqualFold(filepath.Ext(target.Destination), ".json") {
		return nil, errors.New("请输入本机新 JSON 文件的绝对路径。")
	}
	review.Target.Destination = filepath.Clean(target.Destination)
	if _, err := os.Lstat(review.Target.Destination); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("请选择本机不存在的文件路径。")
	}
	plan, err := profiles.previewExport(target.Name)
	if err != nil {
		return nil, errors.New("无法读取所选配置，请核对后重新预览。")
	}
	review.Export = plan
	return review, nil
}

type profileLifecycleReviews struct {
	mu      sync.Mutex
	pending *profileLifecycleReview
}

func (reviews *profileLifecycleReviews) set(review *profileLifecycleReview) {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	reviews.pending = review
}

func (reviews *profileLifecycleReviews) take(token string) *profileLifecycleReview {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	review := reviews.pending
	if review == nil || review.Token != token {
		return nil
	}
	reviews.pending = nil
	if !time.Now().Before(review.expiry) {
		return nil
	}
	return review
}
