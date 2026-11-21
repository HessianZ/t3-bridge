package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func configFeishuGroups(list func(context.Context, string, string, string) ([]bridge.FeishuGroup, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "无法读取应用信息", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		groups, err := list(ctx, strings.TrimSpace(r.PostForm.Get("appId")),
			strings.TrimSpace(r.PostForm.Get("appSecretFile")), strings.TrimSpace(r.PostForm.Get("tenant")))
		if err != nil {
			http.Error(w, "无法读取机器人所在群。请核对应用信息、群聊权限和网络。", http.StatusUnprocessableEntity)
			return
		}
		if groups == nil {
			groups = []bridge.FeishuGroup{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(groups)
	}
}
