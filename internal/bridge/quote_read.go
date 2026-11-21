package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var errQuotedMessageUnavailable = errors.New("visible message unavailable or outside bounds")

func (l *Lark) quotedMessageItems(ctx context.Context, id string, retryAvailable *bool) ([]*im.Message, error) {
	// Keep the response status when a gateway returns a non-JSON body.
	client := l.Channel.RawClient()
	req := &larkcore.ApiReq{
		ApiPath:                   "/open-apis/im/v1/messages/:message_id",
		HttpMethod:                http.MethodGet,
		PathParams:                larkcore.PathParams{"message_id": id},
		QueryParams:               larkcore.QueryParams{"card_msg_content_type": {"user_card_content"}},
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant, larkcore.AccessTokenTypeUser},
	}
	if ctx.Err() != nil {
		return nil, errQuotedMessageUnavailable
	}
	resp, err := client.Do(ctx, req)
	if *retryAvailable && ctx.Err() == nil && transientQuotedMessageRead(resp, err) {
		*retryAvailable = false
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, errQuotedMessageUnavailable
		case <-timer.C:
		}
		retryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		resp, err = client.Do(retryCtx, req)
	}
	if err != nil || resp == nil || resp.StatusCode != http.StatusOK ||
		!strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		return nil, errQuotedMessageUnavailable
	}
	var result struct {
		Code *int                   `json:"code"`
		Data *im.GetMessageRespData `json:"data"`
	}
	if json.Unmarshal(resp.RawBody, &result) != nil || result.Code == nil || *result.Code != 0 || result.Data == nil ||
		len(result.Data.Items) == 0 || len(result.Data.Items) > 20 {
		return nil, errQuotedMessageUnavailable
	}
	return result.Data.Items, nil
}

func transientQuotedMessageRead(resp *larkcore.ApiResp, err error) bool {
	if err != nil {
		var clientTimeout *larkcore.ClientTimeoutError
		var serverTimeout *larkcore.ServerTimeoutError
		return errors.As(err, &clientTimeout) || errors.As(err, &serverTimeout)
	}
	if resp == nil || len(resp.Header.Values("Retry-After")) != 0 {
		return false
	}
	switch resp.StatusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}
