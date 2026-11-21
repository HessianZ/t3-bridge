package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

type t3WorkspaceAuthorization struct {
	mu     sync.Mutex
	id     string
	cancel context.CancelFunc
	view   t3AuthorizationView
}

type t3AuthorizationView struct {
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
	Problem string `json:"problem,omitempty"`
}

func registerT3Authorization(mux *http.ServeMux, workspace context.Context) {
	var session t3WorkspaceAuthorization
	mux.HandleFunc("POST /t3/authorize/start", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Connection bridge.MCPConfig `json:"connection"`
			Replace    bool             `json:"replace"`
		}
		decode := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decode.DisallowUnknownFields()
		if err := decode.Decode(&input); err != nil || decode.Decode(new(json.RawMessage)) != io.EOF {
			http.Error(w, "无法读取 T3 授权信息", http.StatusBadRequest)
			return
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.cancel != nil {
			http.Error(w, "已有 T3 授权在进行", http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(workspace, 5*time.Minute)
		session.id = rand.Text()
		session.cancel = cancel
		session.view = t3AuthorizationView{State: "starting"}
		go func() {
			defer cancel()
			err := authorizeConfiguredT3(ctx, input.Connection, input.Replace, func(address string) error {
				session.mu.Lock()
				defer session.mu.Unlock()
				session.view = t3AuthorizationView{State: "pending", URL: address}
				return nil
			})
			session.mu.Lock()
			defer session.mu.Unlock()
			session.cancel = nil
			switch {
			case err == nil:
				session.view = t3AuthorizationView{State: "saved"}
			case errors.Is(err, context.DeadlineExceeded):
				session.view = t3AuthorizationView{State: "expired"}
			case errors.Is(err, context.Canceled):
				session.view = t3AuthorizationView{State: "canceled"}
			default:
				session.view = t3AuthorizationView{State: "error", Problem: err.Error()}
			}
		}()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": session.id})
	})
	mux.HandleFunc("GET /t3/authorize/status", func(w http.ResponseWriter, r *http.Request) {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.id == "" || session.id != r.URL.Query().Get("id") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(session.view)
	})
	mux.HandleFunc("POST /t3/authorize/cancel", func(w http.ResponseWriter, r *http.Request) {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.id == r.URL.Query().Get("id") && session.cancel != nil {
			session.cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
