package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/HessianZ/t3-bridge/internal/bridge"
)

func lookupWorkspaceReceipt(path, appID, messageID string) (bridge.MessageReceipt, error) {
	if path == "" {
		return bridge.MessageReceipt{}, errors.New("select a saved configuration")
	}
	account, err := loadConfigAccount(path, "", strings.TrimSpace(appID))
	if err != nil {
		return bridge.MessageReceipt{}, err
	}
	return bridge.ReadMessageReceipt(account.StateDir, account.AppID, strings.TrimSpace(messageID))
}

func messageReceiptDeliveryText(state bridge.ReplyDeliveryState) string {
	switch state {
	case bridge.ReplyDeliveryPending:
		return "等待终态回复"
	case bridge.ReplyDeliveryConfirmed:
		return "已获飞书确认"
	case bridge.ReplyDeliveryUnconfirmed:
		return "结果待核对"
	case bridge.ReplyDeliveryNotApplicable:
		return "无需终态回复"
	default:
		return "未知"
	}
}

func runConfigReceipt(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("t3-bridge config receipt", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "saved "+defaultConfigFlagHelp)
	profile := flags.String("profile", "", "use the named configuration profile")
	appID := flags.String("app-id", "", "select an App ID (defaults to the primary account)")
	messageID := flags.String("message-id", "", "incoming Feishu message ID (om_…)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*messageID) == "" {
		return errors.New("config receipt requires -message-id without positional arguments")
	}
	resolved, err := resolveConfigPath(*path, *profile, defaultConfigPath)
	if err != nil {
		return err
	}
	receipt, err := lookupWorkspaceReceipt(resolved, *appID, *messageID)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(receipt)
}
