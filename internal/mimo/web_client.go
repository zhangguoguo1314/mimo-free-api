package mimo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	webBaseURL = "https://aistudio.xiaomimimo.com"
	chatAPI    = "/open-apis/bot/chat"
)

// WebClient 是 MiMo AI Studio 网页端客户端
type WebClient struct {
	httpClient *http.Client
	serviceToken string
	userID      string
	ph          string
}

// NewWebClient 创建网页端客户端
func NewWebClient(serviceToken, userID, ph string) *WebClient {
	// 清理 token 首尾的空格和引号
	serviceToken = strings.TrimSpace(serviceToken)
	serviceToken = strings.Trim(serviceToken, "\"")
	userID = strings.TrimSpace(userID)
	userID = strings.Trim(userID, "\"")
	ph = strings.TrimSpace(ph)
	ph = strings.Trim(ph, "\"")

	return &WebClient{
		httpClient:   &http.Client{Timeout: 30 * time.Minute},
		serviceToken: serviceToken,
		userID:       userID,
		ph:           ph,
	}
}

// WebChatRequest 是网页端请求格式
// 注意：只包含官网实际发送的字段，多余的字段可能导致空回复
type WebChatRequest struct {
	MsgID          string       `json:"msgId"`
	ConversationID string       `json:"conversationId"`
	Query          string       `json:"query"`
	IsEditedQuery  bool         `json:"isEditedQuery"`
	ModelConfig    ModelConfig  `json:"modelConfig"`
	MultiMedias    []interface{} `json:"multiMedias"`
}

// ModelConfig 模型配置
type ModelConfig struct {
	EnableThinking  bool    `json:"enableThinking"`
	WebSearchStatus string  `json:"webSearchStatus"`
	Model           string  `json:"model"`
	Temperature     float64 `json:"temperature,omitempty"`
	TopP            float64 `json:"topP,omitempty"`
}

// Chat 发起聊天，返回 SSE 流
// conversationID: 客户端提供的对话 ID，用于复用 MiMo 对话
// parentID: 上一条 AI 回复的消息 ID，用于维持上下文链
func (c *WebClient) Chat(ctx context.Context, query, model, conversationID, parentID string, thinking bool) (io.ReadCloser, error) {
	if conversationID == "" {
		conversationID = uuid.New().String()
	}

	reqBody := WebChatRequest{
		MsgID:          uuid.New().String(),
		ConversationID: conversationID,
		Query:          query,
		IsEditedQuery:  false,
		ModelConfig: ModelConfig{
			EnableThinking:  thinking,
			WebSearchStatus: "disabled",
			Model:           model,
		},
		MultiMedias: []interface{}{},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	reqURL := fmt.Sprintf("%s%s?xiaomichatbot_ph=%s", webBaseURL, chatAPI, url.QueryEscape(c.ph))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Origin", webBaseURL)
	httpReq.Header.Set("Referer", webBaseURL+"/")
	httpReq.Header.Set("x-timezone", "Asia/Shanghai")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	httpReq.Header.Set("sec-ch-ua", "\"Not/A)Brand\";v=\"8\", \"Chromium\";v=\"126\", \"Google Chrome\";v=\"126\"")
	httpReq.Header.Set("sec-ch-ua-mobile", "?0")
	httpReq.Header.Set("sec-ch-ua-platform", "\"Windows\"")
	httpReq.Header.Set("Cookie", fmt.Sprintf(
		"userId=%s; serviceToken=%s; xiaomichatbot_ph=%s",
		c.userID, c.serviceToken, c.ph,
	))

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mimo returned %d: %s", resp.StatusCode, string(errBody))
	}

	return resp.Body, nil
}

// WebSSEEvent 是网页端 SSE 事件
type WebSSEEvent struct {
	ID    string
	Event string
	Data  string
}

// ParseWebSSE 解析网页端 SSE 流
func ParseWebSSE(ctx context.Context, reader io.ReadCloser, events chan<- WebSSEEvent) error {
	defer reader.Close()
	// 使用 bufio.Reader 逐行读取，支持 \n 和 \r\n
	br := bufio.NewReader(reader)

	var event WebSSEEvent
	eventCount := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if event.Event != "" || event.Data != "" {
				events <- event
				eventCount++
				event = WebSSEEvent{}
			}
			continue
		}

		if strings.HasPrefix(line, "id:") {
			event.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		} else if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			event.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	// 发送最后一个事件（如果没有空行结尾）
	if event.Event != "" || event.Data != "" {
		events <- event
		eventCount++
	}
	log.Printf("[ParseWebSSE] parsed %d events", eventCount)
	return nil
}

// SaveConversation 保存对话到 MiMo 官网（维持服务端上下文的关键）
func (c *WebClient) SaveConversation(ctx context.Context, conversationID, query string) {
	encodedPh := url.QueryEscape(c.ph)
	saveURL := fmt.Sprintf("%s/open-apis/chat/conversation/save?xiaomichatbot_ph=%s", webBaseURL, encodedPh)

	title := query
	if len(title) > 30 {
		title = title[:30]
	}
	savePayload := map[string]interface{}{
		"conversationId": conversationID,
		"title":          title,
		"type":           "chat",
		"multiMedias":    []interface{}{},
	}
	body, _ := json.Marshal(savePayload)

	req, err := http.NewRequestWithContext(ctx, "POST", saveURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("[save] create request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", webBaseURL)
	req.Header.Set("Referer", webBaseURL+"/")
	req.Header.Set("x-timezone", "Asia/Shanghai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", fmt.Sprintf(
		"userId=%s; serviceToken=%s; xiaomichatbot_ph=%s",
		c.userID, c.serviceToken, c.ph,
	))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[save] request error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[save] conv=%s status=%d body=%s", conversationID, resp.StatusCode, string(respBody))
	}
}

// Validate 验证 Cookie 是否有效（通过发送一条简短chat消息验证，避免user/info端点导致进程崩溃）
func (c *WebClient) Validate(ctx context.Context) error {
	validateClient := &http.Client{Timeout: 15 * time.Second}

	encodedPh := url.QueryEscape(c.ph)
	reqURL := fmt.Sprintf("%s/open-apis/bot/chat?xiaomichatbot_ph=%s", webBaseURL, encodedPh)

	reqBody := map[string]interface{}{
		"msgId":          uuid.New().String(),
		"conversationId": uuid.New().String(),
		"query":          "hi",
		"isEditedQuery":  false,
		"modelConfig": map[string]interface{}{
			"enableThinking":  false,
			"webSearchStatus": "disabled",
			"model":           "mimo-v2.5",
		},
		"multiMedias": []interface{}{},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", webBaseURL)
	req.Header.Set("Referer", webBaseURL+"/")
	req.Header.Set("x-timezone", "Asia/Shanghai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", fmt.Sprintf(
		"userId=%s; serviceToken=%s; xiaomichatbot_ph=%s",
		c.userID, c.serviceToken, c.ph,
	))
	resp, err := validateClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 只读取少量数据确认响应开始，避免SSE长连接阻塞
	io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("invalid: status %d", resp.StatusCode)
	}
	return nil
}
