package mimo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
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

// WebChatRequest 网页端聊天请求
// 注意：只包含官网实际发送的字段，多余的字段可能导致空回复
type WebChatRequest struct {
	MsgID          string       `json:"msgId"`
	ConversationID string       `json:"conversationId"`
	ParentID       string       `json:"parentId,omitempty"`
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
// multiMedias: 多媒体文件列表（图片等），可为nil
func (c *WebClient) Chat(ctx context.Context, query, model, conversationID, parentID string, thinking bool, multiMedias []MultiMedia) (io.ReadCloser, error) {
	if conversationID == "" {
		conversationID = uuid.New().String()
	}

	reqBody := WebChatRequest{
		MsgID:          uuid.New().String(),
		ConversationID: conversationID,
		ParentID:       parentID,
		Query:          query,
		IsEditedQuery:  false,
		ModelConfig: ModelConfig{
			EnableThinking:  thinking,
			WebSearchStatus: "disabled",
			Model:           model,
		},
		MultiMedias:    multiMediasSlice(multiMedias),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	// multiMedias info will be returned via response header by the caller (HF Space server layer)
	_ = multiMedias

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
	defer close(events) // Critical: close channel so range loop in consumer exits
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

		// Set read deadline to prevent permanent blocking if MiMo stops sending data
		if conn, ok := reader.(interface{ SetReadDeadline(time.Time) error }); ok {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
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

// MultiMedia represents a media file in MiMo's chat request
type MultiMedia struct {
	MediaType           string `json:"mediaType"`
	Name                string `json:"name"`
	Size                int64  `json:"size"`
	URL                 string `json:"url,omitempty"`
	FileURL             string `json:"fileUrl,omitempty"`
	CompressedVideoURL  string `json:"compressedVideoUrl,omitempty"`
	AudioTrackURL       string `json:"audioTrackUrl,omitempty"`
	ObjectName          string `json:"objectName,omitempty"`
	TokenUsage          int    `json:"tokenUsage,omitempty"`
	Status              string `json:"status,omitempty"`
}

func multiMediasSlice(medias []MultiMedia) []interface{} {
	if len(medias) == 0 {
		return []interface{}{}
	}
	result := make([]interface{}, len(medias))
	for i, m := range medias {
		result[i] = m
	}
	return result
}

// UploadInfoResponse is the response from genUploadInfo (wrapped in data field)
type UploadInfoResponse struct {
	Code int              `json:"code"`
	Msg string           `json:"msg"`
	Data UploadInfoData  `json:"data"`
}

// UploadInfoData contains the actual upload information
type UploadInfoData struct {
	UploadURL   string `json:"uploadUrl"`
	ResourceURL string `json:"resourceUrl"`
	ObjectName  string `json:"objectName"`
	ResourceID  string `json:"resourceId"`
}

// ParseResponse is the response from resource/parse
type ParseResponse struct {
	Code int `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ID         string `json:"id"`
		TokenUsage int    `json:"tokenUsage"`
	} `json:"data"`
}

// UploadMedia uploads a media file to MiMo's storage and returns the resource URL.
// Steps: 1) genUploadInfo 2) PUT upload 3) return resourceUrl
func (c *WebClient) UploadMedia(ctx context.Context, data []byte, fileName, mediaType string) (*MultiMedia, error) {
	// Step 1: Get upload URL
	hash := md5.Sum(data)
	hashStr := fmt.Sprintf("%x", hash)

	uploadReq := map[string]string{
		"fileName":     fileName,
		"fileContentMd5": hashStr,
	}
	uploadBody, _ := json.Marshal(uploadReq)

	encodedPh := url.QueryEscape(c.ph)
	genURL := fmt.Sprintf("%s/open-apis/resource/genUploadInfo?xiaomichatbot_ph=%s", webBaseURL, encodedPh)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", genURL, bytes.NewReader(uploadBody))
	if err != nil {
		return nil, fmt.Errorf("genUploadInfo request: %w", err)
	}
	c.setCommonHeaders(httpReq)

	genResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("genUploadInfo: %w", err)
	}
	defer genResp.Body.Close()

	if genResp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(genResp.Body)
		return nil, fmt.Errorf("genUploadInfo status %d: %s", genResp.StatusCode, string(errBody))
	}

	// Read full response body for debugging
	genBody, _ := io.ReadAll(genResp.Body)
	log.Printf("[upload] genUploadInfo response: %s", string(genBody[:min(len(genBody), 200)]))

	var uploadInfo UploadInfoResponse
	if err := json.Unmarshal(genBody, &uploadInfo); err != nil {
		return nil, fmt.Errorf("genUploadInfo decode: %w, body: %s", err, string(genBody))
	}

	if uploadInfo.Data.UploadURL == "" {
		return nil, fmt.Errorf("genUploadInfo: empty uploadUrl, full response: %s", string(genBody))
	}

	log.Printf("[upload] got upload URL for %s: objectName=%s", fileName, uploadInfo.Data.ObjectName)

	// Step 2: PUT upload the file
	putReq, err := http.NewRequestWithContext(ctx, "PUT", uploadInfo.Data.UploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("upload put request: %w", err)
	}
	putReq.Header.Set("Content-Type", "application/octet-stream")
	putReq.Header.Set("Content-MD5", hashStr)

	putResp, err := c.httpClient.Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("upload put: %w", err)
	}
	defer putResp.Body.Close()
	io.Copy(io.Discard, putResp.Body)

	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("upload put status %d", putResp.StatusCode)
	}

	log.Printf("[upload] uploaded %s (%d bytes) -> %s", fileName, len(data), uploadInfo.Data.ResourceURL)

	// Step 3: Parse the file (required for images to be recognized by MiMo)
	// Must include xiaomichatbot_ph query parameter
	parseURL := fmt.Sprintf("%s/open-apis/resource/parse?fileUrl=%s&objectName=%s&model=mimo-v2.5-pro&xiaomichatbot_ph=%s",
		webBaseURL,
		url.QueryEscape(uploadInfo.Data.ResourceURL),
		url.QueryEscape(uploadInfo.Data.ObjectName),
		url.QueryEscape(c.ph))

	var resourceID string
	var lastParseData struct {
		ID         string `json:"id"`
		TokenUsage int    `json:"tokenUsage"`
	}
	for retry := 0; retry < 3; retry++ {
		parseReq, err := http.NewRequestWithContext(ctx, "POST", parseURL, bytes.NewReader([]byte("{}")))
		if err != nil {
			log.Printf("[upload] create parse request failed: %v", err)
			break
		}
		c.setCommonHeaders(parseReq)

		parseResp, err := c.httpClient.Do(parseReq)
		if err != nil {
			log.Printf("[upload] parse request failed (attempt %d): %v", retry+1, err)
			time.Sleep(2 * time.Second)
			continue
		}

		parseBody, _ := io.ReadAll(parseResp.Body)
		parseResp.Body.Close()

		if parseResp.StatusCode != http.StatusOK {
			log.Printf("[upload] parse status %d (attempt %d): %s", parseResp.StatusCode, retry+1, string(parseBody[:min(len(parseBody), 300)]))
			time.Sleep(2 * time.Second)
			continue
		}

		var parseResult ParseResponse
		if err := json.Unmarshal(parseBody, &parseResult); err != nil {
			log.Printf("[upload] parse decode error: %v, body: %s", err, string(parseBody[:min(len(parseBody), 200)]))
			break
		}

		if parseResult.Data.ID != "" {
			resourceID = parseResult.Data.ID
			lastParseData = parseResult.Data
			log.Printf("[upload] parse success: resourceId=%s", resourceID)
			break
		}

		log.Printf("[upload] parse returned empty id (attempt %d): %s", retry+1, string(parseBody[:min(len(parseBody), 200)]))
		time.Sleep(2 * time.Second)
	}

	if resourceID == "" {
		log.Printf("[upload] WARNING: parse failed to return resourceId, using resourceUrl as fallback")
		resourceID = uploadInfo.Data.ResourceURL
	}

	// Step 4: Return MultiMedia with correct fields
	// CRITICAL: url field = resourceId (from parse), NOT resourceUrl
	// fileUrl field = resourceUrl (the actual file URL)
	tokenUsage := 106 // Default token usage for images
	if lastParseData.ID != "" && lastParseData.TokenUsage > 0 {
		tokenUsage = lastParseData.TokenUsage
	}
	
	return &MultiMedia{
		MediaType:          mediaType,
		Name:               fileName,
		Size:               int64(len(data)),
		URL:                resourceID,
		FileURL:            uploadInfo.Data.ResourceURL,
		CompressedVideoURL: "",
		AudioTrackURL:      "",
		ObjectName:         uploadInfo.Data.ObjectName,
		TokenUsage:         tokenUsage,
		Status:             "completed",
	}, nil
}

// setCommonHeaders sets the common headers for MiMo API requests
func (c *WebClient) setCommonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", webBaseURL)
	req.Header.Set("Referer", webBaseURL+"/")
	req.Header.Set("x-timezone", "Asia/Shanghai")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("sec-ch-ua", "\"Not/A)Brand\";v=\"8\", \"Chromium\";v=\"126\", \"Google Chrome\";v=\"126\"")
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", "\"Windows\"")
	req.Header.Set("Cookie", fmt.Sprintf(
		"userId=%s; serviceToken=%s; xiaomichatbot_ph=%s",
		c.userID, c.serviceToken, c.ph,
	))
}

// ParseDataURI extracts the media type and base64 data from a data URI.
// Returns ("image/png", base64data, filename, error)
func ParseDataURI(dataURI string) (string, []byte, string, error) {
	if !strings.HasPrefix(dataURI, "data:") {
		return "", nil, "", fmt.Errorf("not a data URI")
	}

	// data:image/png;base64,iVBOR...
	parts := strings.SplitN(dataURI[5:], ",", 2)
	if len(parts) != 2 {
		return "", nil, "", fmt.Errorf("invalid data URI format")
	}

	meta := parts[0] // e.g., "image/png;base64"
	b64Data := parts[1]

	// Extract media type and check for base64 encoding
	var mediaType string
	isBase64 := false
	for _, part := range strings.Split(meta, ";") {
		part = strings.TrimSpace(part)
		if part == "base64" {
			isBase64 = true
		} else if strings.Contains(part, "/") {
			mediaType = part
		}
	}

	if !isBase64 {
		return "", nil, "", fmt.Errorf("data URI is not base64 encoded")
	}

	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		// Try raw encoding
		data, err = base64.RawStdEncoding.DecodeString(b64Data)
		if err != nil {
			return "", nil, "", fmt.Errorf("base64 decode: %w", err)
		}
	}

	// Generate filename from media type
	ext := "bin"
	switch mediaType {
	case "image/jpeg", "image/jpg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	case "image/gif":
		ext = "gif"
	case "image/webp":
		ext = "webp"
	case "image/svg+xml":
		ext = "svg"
	case "application/pdf":
		ext = "pdf"
	case "audio/mpeg", "audio/mp3":
		ext = "mp3"
	case "video/mp4":
		ext = "mp4"
	}
	fileName := fmt.Sprintf("upload_%d.%s", time.Now().UnixNano(), ext)

	return mediaType, data, fileName, nil
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
