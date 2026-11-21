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

//go:embed config_credentials_web.html
var configCredentialsHTML string

const credentialReviewTTL = 5 * time.Minute

type configCredentialsPage struct {
	Sources        []webSource
	Source         webSource
	Items          []appCredential
	Selected       *appCredential
	Review         *credentialWebReview
	Message, Error string
}

type credentialWebReview struct {
	Token, Action        string
	sourceID, sourcePath string
	item                 appCredential
	expires              time.Time
}

type credentialWebReviews struct {
	mu      sync.Mutex
	pending *credentialWebReview
}

func configCredentialsHandler(sources func() ([]webSource, error)) http.HandlerFunc {
	page := newWorkspacePage("credentials").Parse(configCredentialsHTML)
	var reviews credentialWebReviews
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		// Native same-origin form POSTs need a non-opaque Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		var id, appID, action string
		if r.Method == http.MethodPost {
			var status int
			action, status = readCredentialWebForm(w, r)
			if status != http.StatusOK {
				http.Error(w, "凭据表单无效或过大", status)
				return
			}
			id, appID = r.PostForm.Get("source"), r.PostForm.Get("appId")
		} else {
			query := r.URL.Query()
			id, appID = query.Get("source"), query.Get("appId")
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置，请刷新后重试", http.StatusConflict)
			return
		}
		view := configCredentialsPage{}
		for _, source := range available {
			if !filepath.IsAbs(source.Path) {
				continue
			}
			if info, err := os.Lstat(source.Path); err != nil || !info.Mode().IsRegular() {
				continue
			}
			view.Sources = append(view.Sources, source)
			if id == "" {
				id = source.ID
			}
			if source.ID == id {
				view.Source = source
			}
		}

		status := http.StatusOK
		switch action {
		case "set", "remove", "cancel":
			review := reviews.take(view.Source, appID, action, r.PostForm.Get("review"))
			switch {
			case review == nil:
				status, view.Error = http.StatusConflict, "凭据预览已失效，请刷新并重新预览。"
			case action == "cancel":
				query := url.Values{"source": {view.Source.ID}, "appId": {review.item.AppID}}
				http.Redirect(w, r, "/credentials?"+query.Encode(), http.StatusSeeOther)
				return
			default:
				var value string
				if action == "set" {
					value = r.PostForm.Get("secret")
				}
				err := review.item.update(action, value)
				switch {
				case errors.Is(err, errAppCredentialConflict):
					status, view.Error = http.StatusConflict, "配置或凭据已变化，请刷新并重新预览。"
				case err != nil:
					status, view.Error = http.StatusUnprocessableEntity, "无法确认凭据操作结果，请刷新核对文件状态。"
				case action == "set":
					view.Message = "已保存本机 App Secret。"
				default:
					view.Message = "已删除本机 App Secret 文件。"
				}
			}
		}

		if view.Source.ID != "" {
			view.Items, err = listAppCredentials(view.Source.Path)
			if err != nil && view.Error == "" {
				status, view.Error = http.StatusConflict, "无法读取所选配置，请刷新或重新选择。"
			}
			if appID != "" {
				if item, err := selectAppCredential(view.Items, appID); err == nil {
					view.Selected = &item
				}
			}
		}
		if operation, preview := strings.CutPrefix(action, "preview-"); preview && view.Error == "" {
			switch {
			case view.Source.ID == "" || view.Selected == nil:
				status, view.Error = http.StatusConflict, "所选配置或 App 不可用，请刷新并重新选择。"
			case !view.Selected.Editable || (operation == "remove" && view.Selected.Status == "missing"):
				status, view.Error = http.StatusUnprocessableEntity, "此 App 的凭据无法执行所选操作。"
			default:
				view.Review = reviews.issue(view.Source, *view.Selected, operation)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}

func readCredentialWebForm(w http.ResponseWriter, r *http.Request) (string, int) {
	// URL-encoded forms can use three bytes per byte of the submitted value.
	r.Body = http.MaxBytesReader(w, r.Body, 3*maxAppCredentialBytes+4096)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return "", http.StatusRequestEntityTooLarge
		}
		return "", http.StatusBadRequest
	}
	action := r.PostForm.Get("action")
	fields := []string{"source", "appId", "action"}
	switch action {
	case "preview-set", "preview-remove":
	case "cancel":
		fields = append(fields, "review")
	case "set":
		fields = append(fields, "review", "secret")
	case "remove":
		if r.PostForm.Get("confirm") != "remove" {
			return "", http.StatusBadRequest
		}
		fields = append(fields, "review", "confirm")
	default:
		return "", http.StatusBadRequest
	}
	if r.URL.RawQuery != "" || len(r.PostForm) != len(fields) {
		return "", http.StatusBadRequest
	}
	for _, field := range fields {
		if len(r.PostForm[field]) != 1 || r.PostForm.Get(field) == "" {
			return "", http.StatusBadRequest
		}
	}
	if action == "set" && len(r.PostForm.Get("secret")) >= maxAppCredentialBytes {
		return "", http.StatusRequestEntityTooLarge
	}
	return action, http.StatusOK
}

func (reviews *credentialWebReviews) issue(source webSource, item appCredential, action string) *credentialWebReview {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	reviews.pending = &credentialWebReview{
		Token: rand.Text(), Action: action, sourceID: source.ID, sourcePath: source.Path,
		item: item, expires: time.Now().Add(credentialReviewTTL),
	}
	return reviews.pending
}

func (reviews *credentialWebReviews) take(source webSource, appID, action, token string) *credentialWebReview {
	reviews.mu.Lock()
	defer reviews.mu.Unlock()
	review := reviews.pending
	if review == nil || review.Token != token {
		return nil
	}
	reviews.pending = nil
	if time.Now().Before(review.expires) && review.sourceID == source.ID && review.sourcePath == source.Path &&
		review.item.AppID == appID && (action == review.Action || action == "cancel") {
		return review
	}
	return nil
}
