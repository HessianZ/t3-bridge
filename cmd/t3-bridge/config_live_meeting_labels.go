package main

func meetingPhaseText(phase string) string {
	switch phase {
	case "join_pending":
		return "入会结果待核对"
	case "join_accepted":
		return "飞书已受理入会"
	case "join_rejected":
		return "飞书已拒绝自动入会"
	case "leave_pending":
		return "离会结果待核对"
	default:
		return "状态未知"
	}
}

func meetingOriginText(origin string) string {
	switch origin {
	case "local":
		return "本机"
	case "invite":
		return "飞书邀请"
	case "chat":
		return "飞书私聊"
	default:
		return "来源未知"
	}
}

func meetingOutcomeText(outcome string) string {
	switch outcome {
	case "joined":
		return "飞书已受理入会。请在参会名单或等候室核对。"
	case "ended":
		return "会议已结束，入会回执已关闭。"
	case "left":
		return "飞书已确认离会。"
	case "confirmed":
		return "已根据你的确认关闭本地会议回执。"
	case "pending":
		return "会议结果待核对。请在参会名单或等候室核对，再决定后续操作。"
	case "rejected":
		return "飞书拒绝了会议操作。请核对会议状态和 App 权限。"
	case "conflict":
		return "会议回执已变化或当前阶段不允许执行。请刷新后核对。"
	case "unavailable":
		return "当前 App 的会议操作不可用。请核对接收状态和会议配置。"
	case "":
		return ""
	default:
		return "会议结果未确认。请刷新并核对参会名单或等候室。"
	}
}
