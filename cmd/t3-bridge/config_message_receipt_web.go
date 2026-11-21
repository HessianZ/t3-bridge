package main

import (
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

//go:embed config_message_receipt_web.html
var configMessageReceiptHTML string

type configMessageReceiptPage struct {
	Sources          []webSource
	Source           webSource
	AppID, MessageID string
	Receipt          *bridge.MessageReceipt
	Error            string
}

func configMessageReceiptHandler(sources func() ([]webSource, error)) http.HandlerFunc {
	page := newWorkspacePage("message receipt").Funcs(template.FuncMap{
		"deliveryText": messageReceiptDeliveryText,
		"deliveryConfirmed": func(state bridge.ReplyDeliveryState) bool {
			return state == bridge.ReplyDeliveryConfirmed
		},
	}).Parse(configMessageReceiptHTML)
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		// Native same-origin form POSTs need a non-opaque Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		view := configMessageReceiptPage{}
		id := r.URL.Query().Get("source")
		post := r.Method == http.MethodPost
		if post {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.URL.RawQuery != "" || r.ParseForm() != nil {
				http.Error(w, "回执查询表单无效", http.StatusBadRequest)
				return
			}
			for field, values := range r.PostForm {
				if len(values) != 1 || (field != "source" && field != "appId" && field != "messageId") {
					http.Error(w, "回执查询表单无效", http.StatusBadRequest)
					return
				}
			}
			id = r.PostForm.Get("source")
			view.AppID, view.MessageID = r.PostForm.Get("appId"), strings.TrimSpace(r.PostForm.Get("messageId"))
			if id == "" || view.MessageID == "" {
				http.Error(w, "请选择已保存的配置并填写飞书原消息 ID", http.StatusBadRequest)
				return
			}
		}

		available, err := sources()
		if err != nil {
			http.Error(w, "无法读取已保存配置", http.StatusConflict)
			return
		}
		for _, source := range available {
			if !filepath.IsAbs(source.Path) {
				continue
			}
			if _, err := bridge.ReadRegularConfigJSON(source.Path); err != nil {
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
		if post {
			if view.Source.ID == "" {
				status, view.Error = http.StatusConflict, "所选配置不可用，请选择已保存的配置。"
			} else {
				receipt, err := lookupWorkspaceReceipt(view.Source.Path, view.AppID, view.MessageID)
				switch {
				case errors.Is(err, bridge.ErrMessageReceiptNotFound):
					status, view.Error = http.StatusNotFound, "所选账号中未找到这条飞书消息的回执。"
				case err != nil:
					status, view.Error = http.StatusUnprocessableEntity, "无法查询回执，请检查所选配置、App ID 和飞书原消息 ID。"
				default:
					view.Receipt = &receipt
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = page.Execute(w, r, view)
	}
}
