package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
	qrcode "github.com/skip2/go-qrcode"
)

type webUserGroupsService interface {
	Status(context.Context) (bridge.UserGroupsStatus, error)
	Start(context.Context, string) (bridge.UserGroupsChallenge, error)
	Finish(context.Context, string) (bridge.UserGroupsStatus, error)
	Cancel(context.Context, string) error
	List(context.Context, string, string) (bridge.UserGroupsPage, error)
	Invite(context.Context, string) (bridge.UserGroupInvitation, error)
}

type userGroupsWebRequest struct {
	AppID         string `json:"appId"`
	AppSecretFile string `json:"appSecretFile"`
	Tenant        string `json:"tenant"`
	Action        string `json:"action"`
	Purpose       string `json:"purpose"`
	Ticket        string `json:"ticket"`
	Query         string `json:"query"`
	PageToken     string `json:"pageToken"`
	ChatID        string `json:"chatId"`
	Confirm       bool   `json:"confirm"`
}

func configUserGroupsHandler(newService func(bridge.Config) (webUserGroupsService, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !runtimeRequestOriginAllowed(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		var request userGroupsWebRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if r.URL.RawQuery != "" || decoder.Decode(&request) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF ||
			(request.Action == "invite" && !request.Confirm) {
			http.Error(w, "无法读取操作，邀请机器人需要明确确认。", http.StatusBadRequest)
			return
		}
		service, err := newService(bridge.Config{
			AppID: strings.TrimSpace(request.AppID), AppSecretFile: strings.TrimSpace(request.AppSecretFile), Tenant: strings.TrimSpace(request.Tenant),
		})
		if err != nil {
			http.Error(w, "无法使用当前应用。请核对应用配置和本机 lark-cli。", http.StatusUnprocessableEntity)
			return
		}
		timeout := 12 * time.Second
		// Owner lookup, the user command and bot membership lookup can run in
		// sequence. Keep the rest of the workbench on its shorter deadline.
		if http.NewResponseController(w).SetWriteDeadline(time.Now().Add(35*time.Second)) == nil {
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		result, err := request.run(ctx, service)
		if err != nil {
			message := "操作未完成。请核对应用所有者授权、本机 lark-cli 和网络。"
			switch request.Action {
			case "finish":
				message = "授权尚未核实。请在飞书完成授权后重试。票据已过期时可重新授权。"
			case "invite":
				message = "邀请结果未确认。请先重新查询机器人入群状态。"
			}
			http.Error(w, message, http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

func (r userGroupsWebRequest) run(ctx context.Context, service webUserGroupsService) (any, error) {
	switch r.Action {
	case "status":
		return service.Status(ctx)
	case "start":
		challenge, err := service.Start(ctx, r.Purpose)
		if err != nil {
			return nil, err
		}
		png, err := qrcode.Encode(challenge.URL, qrcode.Medium, 240)
		if err != nil {
			return nil, err
		}
		return struct {
			bridge.UserGroupsChallenge
			Image string `json:"image"`
		}{challenge, "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, nil
	case "finish":
		return service.Finish(ctx, r.Ticket)
	case "cancel":
		return nil, service.Cancel(ctx, r.Ticket)
	case "list":
		return service.List(ctx, r.Query, r.PageToken)
	case "invite":
		return service.Invite(ctx, r.ChatID)
	default:
		return nil, errors.New("unknown user groups action")
	}
}
