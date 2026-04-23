package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	baseURL     = "https://api.dingtalk.com"
	oapiBaseURL = "https://oapi.dingtalk.com"
)

// DingtalkClient handles communication with the DingTalk API.
type DingtalkClient struct {
	appKey     string
	appSecret  string
	httpClient *http.Client

	mu          sync.Mutex
	accessToken string
	expiry      time.Time

	oapiAccessToken string
	oapiExpiry      time.Time
}

// NewDingtalkClient creates a new DingTalk API client.
func NewDingtalkClient(appKey, appSecret string) *DingtalkClient {
	return &DingtalkClient{
		appKey:    appKey,
		appSecret: appSecret,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Validate verifies the credentials by attempting to fetch an access token.
func (c *DingtalkClient) Validate(ctx context.Context) error {
	_, err := c.GetAccessToken(ctx)
	return err
}

// GetAccessToken returns a valid v1.0 access token.
func (c *DingtalkClient) GetAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.expiry) {
		return c.accessToken, nil
	}

	body := map[string]string{
		"appKey":    c.appKey,
		"appSecret": c.appSecret,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1.0/oauth2/accessToken", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dingtalk auth failed: status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	c.accessToken = result.AccessToken
	// Set expiry with a 5-minute buffer
	c.expiry = time.Now().Add(time.Duration(result.ExpireIn-300) * time.Second)
	return c.accessToken, nil
}

// GetOapiAccessToken returns a valid legacy OAPI access token.
func (c *DingtalkClient) GetOapiAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.oapiAccessToken != "" && time.Now().Before(c.oapiExpiry) {
		return c.oapiAccessToken, nil
	}

	url := fmt.Sprintf("%s/gettoken?appkey=%s&appsecret=%s", oapiBaseURL, c.appKey, c.appSecret)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpireIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if result.ErrCode != 0 {
		return "", fmt.Errorf("dingtalk oapi auth failed: %s", result.ErrMsg)
	}

	c.oapiAccessToken = result.AccessToken
	c.oapiExpiry = time.Now().Add(time.Duration(result.ExpireIn-300) * time.Second)
	return c.oapiAccessToken, nil
}

func (c *DingtalkClient) doRequest(ctx context.Context, method, path string, body any) ([]byte, error) {
	token, err := c.GetAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	hasCodeField := bytes.Contains(respBody, []byte(`"code"`))
	isSuccess := bytes.Contains(respBody, []byte(`"code":"success"`)) ||
		bytes.Contains(respBody, []byte(`"code": "success"`))

	if (resp.StatusCode < 200 || resp.StatusCode >= 300) || (hasCodeField && !isSuccess) {
		slog.Error("dingtalk api call failed", "path", path, "status", resp.StatusCode, "body", string(respBody))
		return respBody, fmt.Errorf("dingtalk api error: status %d body %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// SendOTO sends a private message to one or more users.
func (c *DingtalkClient) SendOTO(ctx context.Context, robotCode string, userIds []string, msgKey string, msgParam string) (string, error) {
	body := map[string]any{
		"robotCode": robotCode,
		"userIds":   userIds,
		"msgKey":    msgKey,
		"msgParam":  msgParam,
	}
	resp, err := c.doRequest(ctx, "POST", "/v1.0/robot/oToMessages/batchSend", body)
	if err != nil {
		return "", err
	}
	var result struct {
		ProcessQueryKey string `json:"processQueryKey"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	return result.ProcessQueryKey, nil
}

// SendGroupMessage sends a message to a group.
func (c *DingtalkClient) SendGroupMessage(ctx context.Context, robotCode string, openConversationId string, msgKey string, msgParam string) (string, error) {
	body := map[string]any{
		"robotCode":          robotCode,
		"openConversationId": openConversationId,
		"msgKey":             msgKey,
		"msgParam":           msgParam,
	}
	resp, err := c.doRequest(ctx, "POST", "/v1.0/robot/groupMessages/send", body)
	if err != nil {
		return "", err
	}
	var result struct {
		ProcessQueryKey string `json:"processQueryKey"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	return result.ProcessQueryKey, nil
}

// AddEmotionReply adds a "thinking" sticker to a user's message.
func (c *DingtalkClient) AddEmotionReply(ctx context.Context, robotCode string, msgID string, conversationID string) error {
	body := map[string]any{
		"robotCode":          robotCode,
		"openMsgId":          msgID,
		"openConversationId": conversationID,
		"emotionType":        2,
		"emotionName":        "🤔思考中",
		"textEmotion": map[string]any{
			"emotionId":    "2659900",
			"emotionName":  "🤔思考中",
			"text":         "🤔思考中",
			"backgroundId": "im_bg_1",
		},
	}
	_, err := c.doRequest(ctx, "POST", "/v1.0/robot/emotion/reply", body)
	return err
}

// RecallEmotionReply removes a "thinking" sticker from a user's message.
func (c *DingtalkClient) RecallEmotionReply(ctx context.Context, robotCode string, msgID string, conversationID string) error {
	body := map[string]any{
		"robotCode":          robotCode,
		"openMsgId":          msgID,
		"openConversationId": conversationID,
		"emotionType":        2,
		"emotionName":        "🤔思考中",
		"textEmotion": map[string]any{
			"emotionId":    "2659900",
			"emotionName":  "🤔思考中",
			"text":         "🤔思考中",
			"backgroundId": "im_bg_1",
		},
	}
	_, err := c.doRequest(ctx, "POST", "/v1.0/robot/emotion/recall", body)
	return err
}

const aiCardTemplateID = "02fcf2f4-5e02-4a85-b672-46d1f715543e.schema"

// CreateAICard creates an AI card.
func (c *DingtalkClient) CreateAICard(ctx context.Context, robotCode string, chatID string, content string, outTrackID string, subTitle string) error {
	body := map[string]interface{}{
		"cardTemplateId": aiCardTemplateID,
		"outTrackId":     outTrackID,
		"cardData": map[string]interface{}{
			"cardParamMap": map[string]interface{}{
				"msgContent":        content,
				"staticMsgContent":  "",
				"subTitle":          subTitle,
				"flowStatus":        "2", // 2=ANSWERING (Active dots animation)
				"sys_full_json_obj": `{"order":["subTitle","msgContent"]}`,
			},
		},
		"callbackType":          "STREAM",
		"imGroupOpenSpaceModel": map[string]any{"supportForward": true},
		"imRobotOpenSpaceModel": map[string]any{"supportForward": true},
	}

	_, err := c.doRequest(ctx, "POST", "/v1.0/card/instances", body)
	if err != nil {
		return fmt.Errorf("create card instance: %w", err)
	}

	// 2. Deliver card
	deliverBody := map[string]any{
		"outTrackId": outTrackID,
		"userIdType": 1,
	}

	// If targetID starts with 'cid', it's a group
	if len(chatID) > 3 && chatID[:3] == "cid" {
		deliverBody["openSpaceId"] = "dtv1.card//IM_GROUP." + chatID
		deliverBody["imGroupOpenDeliverModel"] = map[string]any{"robotCode": robotCode}
	} else {
		deliverBody["openSpaceId"] = "dtv1.card//IM_ROBOT." + chatID
		deliverBody["imRobotOpenDeliverModel"] = map[string]any{
			"spaceType": "IM_ROBOT",
			"robotCode": robotCode,
			"extension": map[string]any{"dynamicSummary": "true"},
		}
	}

	_, err = c.doRequest(ctx, "POST", "/v1.0/card/instances/deliver", deliverBody)
	if err != nil {
		return fmt.Errorf("deliver card: %w", err)
	}

	return nil
}

// UpdateAICard updates an existing AI card.
func (c *DingtalkClient) UpdateAICard(ctx context.Context, outTrackID string, content string, subTitle string, flowStatus string, finished bool) error {
	if !finished {
		// 流式阶段：只调用 streaming 接口
		body := map[string]interface{}{
			"outTrackId": outTrackID,
			"guid":       fmt.Sprintf("g_%d", time.Now().UnixNano()),
			"key":        "msgContent",
			"content":    content,
			"isFull":     true,
			"isFinalize": false,
			"isError":    false,
		}

		_, err := c.doRequest(ctx, "PUT", "/v1.0/card/streaming", body)
		return err
	}

	// 完成阶段：Step 1 先发 isFinalize=true 关闭流式通道
	finalizeBody := map[string]interface{}{
		"outTrackId": outTrackID,
		"guid":       fmt.Sprintf("g_fin_%d", time.Now().UnixNano()),
		"key":        "msgContent",
		"content":    content,
		"isFull":     true,
		"isFinalize": true, // ← 关键，关闭流式通道
		"isError":    false,
	}
	_, _ = c.doRequest(ctx, "PUT", "/v1.0/card/streaming", finalizeBody)

	// Step 2 再更新 instances 设置 FINISHED 状态
	statusBody := map[string]any{
		"outTrackId": outTrackID,
		"cardData": map[string]any{
			"cardParamMap": map[string]interface{}{
				"flowStatus":        flowStatus,
				"msgContent":        content,
				"subTitle":          subTitle,
				"staticMsgContent":  "",
				"config":            `{"autoLayout": true}`,
				"sys_full_json_obj": `{"order":["subTitle","msgContent"]}`,
			},
		},
		"cardUpdateOptions": map[string]any{
			"updateCardDataByKey": true,
		},
	}
	_, err := c.doRequest(ctx, "PUT", "/v1.0/card/instances", statusBody)
	return err
}

// SendMessage sends a text message to a user or group.
func (c *DingtalkClient) SendMessage(ctx context.Context, robotCode string, targetID string, text string) error {
	msgParam, _ := json.Marshal(map[string]string{"content": text})

	if len(targetID) > 3 && targetID[:3] == "cid" {
		_, err := c.SendGroupMessage(ctx, robotCode, targetID, "sampleText", string(msgParam))
		return err
	}

	_, err := c.SendOTO(ctx, robotCode, []string{targetID}, "sampleText", string(msgParam))
	return err
}
