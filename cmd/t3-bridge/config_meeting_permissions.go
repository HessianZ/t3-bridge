package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

const meetingPermissionTimeout = 10 * time.Second

var errMeetingPermissionSourceChanged = errors.New("已保存的配置已变化或不可用，请重新打开后检查")

type meetingPermissionChoice struct{ AppID, Tenant string }

func savedMeetingPermissionChoices(path string) (revision string, choices []meetingPermissionChoice, err error) {
	if !filepath.IsAbs(path) {
		return "", nil, errMeetingPermissionSourceChanged
	}
	raw, err := bridge.ReadRegularConfigJSON(path)
	if err != nil {
		return "", nil, err
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return "", nil, err
	}
	for _, account := range config.Accounts() {
		identity := newReceiverGroupAccount(account.Tenant, account.AppID)
		choices = append(choices, meetingPermissionChoice{AppID: identity.AppID, Tenant: identity.Tenant})
	}
	return configRevision(raw), choices, nil
}

func checkSavedMeetingPermissions(ctx context.Context, source webSource, appID, tenant string) (bridge.MeetingPermissionReport, error) {
	return verifySavedMeetingPermissions(ctx, source, newReceiverGroupAccount(tenant, appID), bridge.CheckMeetingPermissions)
}

func checkSavedMeetingAvailability(ctx context.Context, source webSource, appID, tenant string) (bridge.MeetingPermissionReport, error) {
	return verifySavedMeetingPermissions(ctx, source, newReceiverGroupAccount(tenant, appID), bridge.CheckMeetingAvailability)
}

func verifySavedMeetingPermissions(ctx context.Context, source webSource, identity receiverProcessAccount,
	check func(context.Context, bridge.Config) (bridge.MeetingPermissionReport, error),
) (bridge.MeetingPermissionReport, error) {
	if !filepath.IsAbs(source.Path) || source.Revision == "" || source.Revision == "missing" {
		return bridge.MeetingPermissionReport{}, errMeetingPermissionSourceChanged
	}
	raw, err := bridge.ReadRegularConfigJSON(source.Path)
	if err != nil || configRevision(raw) != source.Revision {
		return bridge.MeetingPermissionReport{}, errMeetingPermissionSourceChanged
	}
	config, err := bridge.ParseConfig(raw)
	if err != nil {
		return bridge.MeetingPermissionReport{}, errors.New("请先校验并保存有效的配置")
	}
	for _, account := range config.Accounts() {
		if newReceiverGroupAccount(account.Tenant, account.AppID) != identity {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, meetingPermissionTimeout)
		defer cancel()
		if err := probeCtx.Err(); err != nil {
			return bridge.MeetingPermissionReport{}, err
		}
		report, err := check(probeCtx, account)
		latest, readErr := bridge.ReadRegularConfigJSON(source.Path)
		if readErr != nil || configRevision(latest) != source.Revision {
			return bridge.MeetingPermissionReport{}, errMeetingPermissionSourceChanged
		}
		return report, err
	}
	return bridge.MeetingPermissionReport{}, errors.New("已保存的配置中没有所选租户及 App")
}

func meetingPermissionStateText(state string) string {
	switch state {
	case "granted":
		return "已授权"
	case "missing":
		return "未授权"
	default:
		return "未知"
	}
}

func meetingPermissionScopeText(scope string) string {
	switch scope {
	case "vc:meeting.bot.join:write":
		return "机器人入会和离会"
	case "vc:meeting.meetingevent:read":
		return "读取会议事件与字幕"
	case "vc:meeting.message:write":
		return "发送会中消息"
	default:
		return "会议权限"
	}
}

func meetingPublicationStateText(state string) string {
	switch state {
	case "verified":
		return "已核对线上版本"
	case "unpublished":
		return "尚无已发布版本"
	default:
		return "线上版本未知"
	}
}

func meetingEventStateText(state string) string {
	switch state {
	case "subscribed":
		return "已订阅"
	case "missing":
		return "未订阅"
	default:
		return "未知"
	}
}

func meetingAvailabilityText(state string) string {
	switch state {
	case "readable":
		return "活动会议接口可查询"
	case "not-in-beta":
		return "平台暂未开放此接口"
	case "missing-scope":
		return "接口所需权限不足"
	case "owner-unavailable":
		return "应用所有者未知，无法探测接口"
	default:
		return "接口状态未知"
	}
}
