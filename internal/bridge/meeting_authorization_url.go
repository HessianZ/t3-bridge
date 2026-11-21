package bridge

import "net/url"

func meetingAuthorizationURL(tenant, appID, scope string) string {
	if !userGroupsAppID.MatchString(appID) {
		return ""
	}
	switch scope {
	case "vc:meeting.bot.join:write", "vc:meeting.meetingevent:read", "vc:meeting.message:write":
	default:
		return ""
	}
	host := "open.feishu.cn"
	switch tenant {
	case "", "feishu":
	case "lark":
		host = "open.larksuite.com"
	default:
		return ""
	}
	link := url.URL{Scheme: "https", Host: host, Path: "/app/" + appID + "/auth", RawQuery: url.Values{"q": {scope}}.Encode()}
	return link.String()
}
