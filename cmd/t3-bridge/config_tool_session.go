package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"net/http"
)

//go:embed config_tool_session.js
var configToolSessionJS string

func configToolSession(host string, handler http.Handler) (string, http.Handler) {
	key := rand.Text()
	return "http://" + host + "/#workspaceSession=" + key, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/tool-identity" {
			w.Header().Set("Cache-Control", "no-store")
			if r.Host != host || !runtimeRequestOriginAllowed(r) {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "无法读取核验请求", http.StatusBadRequest)
				return
			}
			values := r.PostForm["workspaceSession"]
			if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(key)) != 1 {
				http.Error(w, "请从终端打印的完整地址打开核验页面，或重新打开桌面工作台。", http.StatusForbidden)
				return
			}
			// The selected-source handler validates just the action's own fields.
			r.PostForm.Del("workspaceSession")
			r.Form.Del("workspaceSession")
		}
		handler.ServeHTTP(w, r)
	})
}
