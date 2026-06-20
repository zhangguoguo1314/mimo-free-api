package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zhangguoguo1314/mimo-free-api/internal/adapter"
	"github.com/zhangguoguo1314/mimo-free-api/internal/convstore"
	"github.com/zhangguoguo1314/mimo-free-api/internal/mimo"
	"github.com/zhangguoguo1314/mimo-free-api/internal/pool"
	"github.com/zhangguoguo1314/mimo-free-api/internal/promptcompat"
	"github.com/zhangguoguo1314/mimo-free-api/internal/router"
	"github.com/zhangguoguo1314/mimo-free-api/internal/stats"
	"github.com/zhangguoguo1314/mimo-free-api/internal/toolcall"
)

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max returns the larger of a and b.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type ChatHandler struct {
	pool      *pool.Pool
	convStore *convstore.Store
}

func NewChatHandler(p *pool.Pool, cs *convstore.Store) *ChatHandler {
	return &ChatHandler{pool: p, convStore: cs}
}

// parsedEvent 解析后的 SSE 事件
type parsedEvent struct {
	Event string
	Data  string
}

// usageData MiMo usage 事件结构
type usageData struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	NativeUsage      *nativeUsage `json:"nativeUsage,omitempty"`
}

type nativeUsage struct {
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	TotalTokens      int              `json:"total_tokens"`
	PromptDetails    *promptDetails   `json:"prompt_tokens_details,omitempty"`
	CompletionDetails *completionDetails `json:"completion_tokens_details,omitempty"`
}

type promptDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type completionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// dialogIdData dialogId 事件结构
type dialogIdData struct {
	Content string `json:"content"`
}

func (h *ChatHandler) Handle(w http.ResponseWriter, r *http.Request) {
	var req adapter.OpenAIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	routeResult := router.RouteModel(req.Model, toMiMoMessages(req.Messages))
	log.Printf("[route] model=%s reason=%s", routeResult.Model, routeResult.Reason)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	if !h.pool.HasAccounts() {
		writeError(w, http.StatusServiceUnavailable, "no accounts configured")
		return
	}

	h.handleWebChat(ctx, w, &req, routeResult.Model, req.Stream)
}

// handleWebChat 使用网页端反代 — 有状态模式（复用 MiMo conversationId + parentId）
// 支持空回复自动重试：检测到空内容时切换账号重试，最多重试2次
func (h *ChatHandler) handleWebChat(ctx context.Context, w http.ResponseWriter, req *adapter.OpenAIChatRequest, model string, stream bool) {
	// Extract latest user message as query (not full history)
	query := extractLatestOpenAIUserMessage(req.Messages)
	if query == "" {
		log.Printf("[filter] all user messages auto-generated, returning empty response")
		writeError(w, http.StatusBadRequest, "no valid user message")
		return
	}

	firstMsg := extractFirstOpenAIUserMessage(req.Messages)
	key := convstore.DeriveKey(firstMsg, model)

	if len(req.Tools) > 0 {
		toolPrompt := buildToolPrompt(req.Tools)
		query = toolPrompt + "\n\n" + query
	}

	maxRetries := 2
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("[retry] attempt %d/%d after empty response", attempt, maxRetries)
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		client, release, err := h.pool.Acquire()
		if err != nil {
			if attempt == maxRetries {
				writeError(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			continue
		}

		convID, parentID := h.convStore.GetOrCreate(key)

		stats.Get().IncrConcurrency()
		body, err := client.Chat(ctx, query, model, convID, parentID, false)
		if err != nil {
			stats.Get().DecrConcurrency()
			release()
			handleChatError(h.pool, client, err)
			if attempt == maxRetries {
				writeError(w, http.StatusBadGateway, fmt.Sprintf("mimo error: %v", err))
				return
			}
			continue
		}

		events := make(chan mimo.WebSSEEvent, 64)
		go func() {
			defer close(events)
			mimo.ParseWebSSE(ctx, body, events)
		}()

		usageChan := make(chan *usageData, 1)
		dialogChan := make(chan string, 1)
		lastMsgIDChan := make(chan string, 1)
		hasContentChan := make(chan bool, 1)
		msgChan := make(chan mimo.WebSSEEvent, 64)

		go func() {
			defer close(msgChan)
			lastMsgID := ""
			hasContent := false
			msgCount := 0
			for ev := range events {
				if ev.Event == "message" && ev.ID != "" {
					lastMsgID = ev.ID
				}
				if ev.Event == "message" && !hasContent {
					var check struct {
						Type    string `json:"type"`
						Content string `json:"content"`
					}
					if json.Unmarshal([]byte(ev.Data), &check) == nil && check.Type == "text" && check.Content != "" {
						hasContent = true
					}
				}
				switch ev.Event {
				case "usage":
					var u usageData
					if json.Unmarshal([]byte(ev.Data), &u) == nil {
						usageChan <- &u
					}
				case "dialogId":
					var d dialogIdData
					if json.Unmarshal([]byte(ev.Data), &d) == nil {
						dialogChan <- d.Content
					}
				case "message":
					msgCount++
					msgChan <- ev
				default:
					if ev.Event != "" {
						log.Printf("[sse] unknown event type: %q data: %s", ev.Event, ev.Data)
					}
				}
			}
			log.Printf("[sse] total message events: %d, hasContent: %v, lastMsgID: %s", msgCount, hasContent, lastMsgID)
			close(usageChan)
			close(dialogChan)
			lastMsgIDChan <- lastMsgID
			close(lastMsgIDChan)
			hasContentChan <- hasContent
			close(hasContentChan)
		}()

		if req.Stream {
			h.streamWebToOpenAI(w, model, msgChan, len(req.Tools) > 0)
		} else {
			h.nonStreamWebToOpenAI(w, model, msgChan, len(req.Tools) > 0)
		}

		stats.Get().DecrConcurrency()

		if u := <-usageChan; u != nil {
			cached := 0
			reasoning := 0
			if u.NativeUsage != nil {
				if u.NativeUsage.PromptDetails != nil {
					cached = u.NativeUsage.PromptDetails.CachedTokens
				}
				if u.NativeUsage.CompletionDetails != nil {
					reasoning = u.NativeUsage.CompletionDetails.ReasoningTokens
				}
			}
			stats.Get().Record(model, u.PromptTokens, u.CompletionTokens, cached, reasoning, u.TotalTokens)
			log.Printf("[usage] model=%s prompt=%d completion=%d cached=%d reasoning=%d",
				model, u.PromptTokens, u.CompletionTokens, cached, reasoning)
		}

		hasContent := <-hasContentChan
		if hasContent {
			if convID != "" {
				go client.SaveConversation(context.Background(), convID, query)
			}
			if lastMsgID := <-lastMsgIDChan; lastMsgID != "" {
				h.convStore.SetParentID(key, lastMsgID)
				log.Printf("[conv] updated parentId for key=%s convID=%s: %s", key[:8], convID[:8], lastMsgID[:min(len(lastMsgID), 8)])
			}
			release()
			return
		}

		log.Printf("[retry] empty response from account, will retry")
		release()
		// 标记当前账号可能需要冷却
		h.pool.MarkCooldown(client)
	}

	// 所有重试都失败了
	log.Printf("[retry] all retries exhausted, returning empty response")
	if stream {
		// 流式模式下 header 可能已经发送，无法返回错误
		// 发送一个空的结束 chunk
		flusher, ok := w.(http.Flusher)
		if !ok {
			flusher = &noopFlusher{}
		}
		finishChunk := adapter.MakeOpenAIStreamChunk(model, "", true)
		fmt.Fprintf(w, "data: %s\n\n", finishChunk)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	} else {
		writeError(w, http.StatusBadGateway, "all accounts returned empty response, please try again later")
	}
}

func (h *ChatHandler) streamWebToOpenAI(w http.ResponseWriter, model string, events <-chan mimo.WebSSEEvent, hasTools bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Fallback: buffer all and write at once (should not happen with HTTP/1.1)
		flusher = &noopFlusher{}
	}
	inThinking := false

	var buffered strings.Builder

	writeChunk := func(content string, finish bool) {
		chunk := adapter.MakeOpenAIStreamChunk(model, content, finish)
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
	}

	msgIdx := 0
	var lastErrorMsg string
	for event := range events {
		if event.Event != "message" {
			continue
		}
		var msg struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(event.Data), &msg); err != nil {
			log.Printf("[stream] failed to unmarshal message[%d]: %v, data: %s", msgIdx, err, event.Data)
			continue
		}
		log.Printf("[stream] message[%d] type=%q content_len=%d content=%q", msgIdx, msg.Type, len(msg.Content), msg.Content)
		if msg.Type != "text" {
			if msg.Type == "error" {
				lastErrorMsg = msg.Content
			}
			msgIdx++
			continue
		}
		if msg.Content == "" {
			msgIdx++
			continue
		}
		c := strings.ReplaceAll(msg.Content, "\u0000", "")
		c, inThinking = filterThinkingChunk(c, inThinking)
		if c == "" {
			msgIdx++
			continue
		}
		buffered.WriteString(c)
		// Stream text chunks immediately
		writeChunk(c, false)
		msgIdx++
	}

	finalText := strings.TrimSpace(buffered.String())
	log.Printf("[stream] finalText len=%d content=%q", len(finalText), finalText)
	if finalText == "" && lastErrorMsg != "" {
		log.Printf("[stream] empty text but got error message: %s", lastErrorMsg)
	}
	if hasTools && len(finalText) > 0 {
		log.Printf("[tools] raw output (len=%d): %q", len(finalText), finalText[:min(len(finalText), 500)])
		if toolcall.HasToolCallSyntax(finalText) {
			calls := toolcall.ParseToolCallsFromText(finalText)
			log.Printf("[tools] parsed %d calls from text", len(calls))
			for i, c := range calls {
				log.Printf("[tools] call[%d]: name=%s input=%v", i, c.Name, c.Input)
			}
			if len(calls) > 0 {
				toolCalls := toolcall.ConvertToolCallsToOpenAI(calls)
				log.Printf("[tools] detected %d tool calls in stream", len(toolCalls))
				// First send a finish chunk with empty content to signal end of text
				finishChunk := adapter.MakeOpenAIStreamChunk(model, "", true)
				fmt.Fprintf(w, "data: %s\n\n", finishChunk)
				// Then send the tool_calls chunk
				toolChunk := adapter.MakeOpenAIStreamToolCallChunk(model, toolCalls, true)
				fmt.Fprintf(w, "data: %s\n\n", toolChunk)
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
		}
	}

	writeChunk("", true)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// noopFlusher is a fallback flusher that does nothing
type noopFlusher struct{}

func (n *noopFlusher) Flush() {}

// filterThinkingChunk 状态机方式过滤 thinking 内容
func filterThinkingChunk(content string, inThinking bool) (string, bool) {
	var result strings.Builder

	for len(content) > 0 {
		if inThinking {
			end := strings.Index(content, "</think>")
			if end == -1 {
				return "", true
			}
			content = content[end+8:]
			inThinking = false
			continue
		}

		start := strings.Index(content, "<think>")
		if start == -1 {
			result.WriteString(content)
			break
		}

		result.WriteString(content[:start])
		content = content[start+7:]
		inThinking = true
	}

	return result.String(), inThinking
}

func (h *ChatHandler) nonStreamWebToOpenAI(w http.ResponseWriter, model string, events <-chan mimo.WebSSEEvent, hasTools bool) {
	var content strings.Builder
	inThinking := false

	msgIdx := 0
	var lastErrorMsg string
	for event := range events {
		if event.Event != "message" {
			continue
		}
		var msg struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(event.Data), &msg); err != nil {
			log.Printf("[nonstream] failed to unmarshal message[%d]: %v, data: %s", msgIdx, err, event.Data)
			continue
		}
		log.Printf("[nonstream] message[%d] type=%q content_len=%d content=%q", msgIdx, msg.Type, len(msg.Content), msg.Content)
		if msg.Type != "text" {
			if msg.Type == "error" {
				lastErrorMsg = msg.Content
			}
			msgIdx++
			continue
		}
		if msg.Content != "" {
			c := strings.ReplaceAll(msg.Content, "\u0000", "")
			c, inThinking = filterThinkingChunk(c, inThinking)
			content.WriteString(c)
		}
		msgIdx++
	}

	finalText := strings.TrimSpace(content.String())
	log.Printf("[nonstream] finalText len=%d content=%q", len(finalText), finalText)
	if finalText == "" && lastErrorMsg != "" {
		log.Printf("[nonstream] empty text but got error message: %s", lastErrorMsg)
	}

	// 检测是否包含工具调用
	if hasTools && len(finalText) > 0 {
		log.Printf("[tools] non-stream raw output (len=%d): %q", len(finalText), finalText[:min(len(finalText), 500)])
		if toolcall.HasToolCallSyntax(finalText) {
			calls := toolcall.ParseToolCallsFromText(finalText)
			log.Printf("[tools] non-stream parsed %d calls", len(calls))
			for i, c := range calls {
				log.Printf("[tools] call[%d]: name=%s input=%v", i, c.Name, c.Input)
			}
			if len(calls) > 0 {
				toolCalls := toolcall.ConvertToolCallsToOpenAI(calls)
				log.Printf("[tools] detected %d tool calls in response", len(toolCalls))
				resp := adapter.MakeOpenAIToolCallResponse(model, toolCalls)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(resp)
				return
			}
		}
	}

	resp := adapter.MakeOpenAIResponse(model, finalText)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resp)
}

func toMiMoMessages(msgs []adapter.OpenAIMessage) []mimo.Message {
	result := make([]mimo.Message, len(msgs))
	for i, m := range msgs {
		// MiMo expects string content, extract text from multimodal arrays
		content := extractContentString(m.Content)
		result[i] = mimo.Message{Role: m.Role, Content: content}
	}
	return result
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "invalid_request_error",
			"code":    status,
		},
	})
}

// mimoStatusRe 用于从错误信息 "mimo returned 401: ..." 中提取 HTTP 状态码
var mimoStatusRe = regexp.MustCompile(`mimo returned (\d+)`)

// extractMimoStatusCode 从错误信息中解析 mimo 返回的 HTTP 状态码。
// 错误格式为 fmt.Errorf("mimo returned %d: %s", statusCode, body)。
// 如果无法解析，返回 0。
func extractMimoStatusCode(err error) int {
	if err == nil {
		return 0
	}
	m := mimoStatusRe.FindStringSubmatch(err.Error())
	if len(m) >= 2 {
		var code int
		for _, ch := range m[1] {
			if ch >= '0' && ch <= '9' {
				code = code*10 + int(ch-'0')
			}
		}
		return code
	}
	return 0
}

// handleChatError 根据错误中的 HTTP 状态码进行分级处理。
// - 401/403: 认证失败，标记 Cookie 失效
// - 429: 限流，退避计数
// - 502/503: 临时故障，短冷却
// - 其他/无法解析状态码: 保持原有冷却+不健康标记
func handleChatError(p *pool.Pool, client *mimo.WebClient, err error) {
	statusCode := extractMimoStatusCode(err)
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		log.Printf("[error] web chat status=%d: %v", statusCode, err)
		p.MarkAuthFailed(client)
	case http.StatusTooManyRequests:
		log.Printf("[error] web chat status=%d: %v", statusCode, err)
		p.MarkRateLimit(client)
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		log.Printf("[error] web chat status=%d: %v", statusCode, err)
		p.MarkTempError(client)
	default:
		log.Printf("[error] web chat status=%d: %v", statusCode, err)
		p.MarkCooldown(client)
		p.MarkUnhealthy(client)
	}
}

func ModelsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   router.SupportedModels(),
	})
}

// MessagesHandler Anthropic 格式
type MessagesHandler struct {
	pool      *pool.Pool
	convStore *convstore.Store
}

func NewMessagesHandler(p *pool.Pool, cs *convstore.Store) *MessagesHandler {
	return &MessagesHandler{pool: p, convStore: cs}
}

func (h *MessagesHandler) Handle(w http.ResponseWriter, r *http.Request) {
	var req adapter.AnthropicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	routeResult := router.RouteModel(req.Model, nil)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	if !h.pool.HasAccounts() {
		writeError(w, http.StatusServiceUnavailable, "no accounts configured")
		return
	}

	client, release, err := h.pool.Acquire()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer release()

	// Extract latest user message as query (not full history)
	hasTools := len(req.Tools) > 0
	query := promptcompat.ExtractLatestUserMessage(req.Messages)
	if query == "" {
		// All user messages were auto-generated (e.g. predict next message) — skip
		log.Printf("[filter] all Anthropic user messages auto-generated, returning empty response")
		writeError(w, http.StatusBadRequest, "no valid user message")
		return
	}

	// Look up or create conversation using hash of first message as key
	firstMsg := promptcompat.ExtractFirstUserMessage(req.Messages)
	key := convstore.DeriveKey(firstMsg, routeResult.Model)
	convID, parentID := h.convStore.GetOrCreate(key)

	// Inject tool definitions into query so MiMo knows what tools are available
	if hasTools {
		openaiTools := adapter.ConvertAnthropicToolsToOpenAI(req.Tools)
		toolPrompt := buildToolPrompt(openaiTools)
		query = toolPrompt + "\n\n" + query
		log.Printf("[tools] stateful Anthropic prompt with %d tools, query len=%d, key=%s, convID=%s, parentID=%s",
			len(req.Tools), len(query), key[:8], convID[:8], parentID[:min(len(parentID), 8)])
	}

	stats.Get().IncrConcurrency()
	defer stats.Get().DecrConcurrency()

	body, err := client.Chat(ctx, query, routeResult.Model, convID, parentID, false)
	if err != nil {
		// 根据状态码分级处理
		handleChatError(h.pool, client, err)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("mimo error: %v", err))
		return
	}

	events := make(chan mimo.WebSSEEvent, 64)
	go func() {
		defer close(events)
		mimo.ParseWebSSE(ctx, body, events)
	}()

	// 分发事件
	usageChan := make(chan *usageData, 1)
	msgChan := make(chan mimo.WebSSEEvent, 64)
	lastMsgIDChan := make(chan string, 1)
	hasContentChan := make(chan bool, 1)

	go func() {
		defer close(msgChan)
		lastMsgID := ""
		hasContent := false
		for ev := range events {
			if ev.Event == "message" && ev.ID != "" {
				lastMsgID = ev.ID
			}
			// Track if any non-empty text content was received
			if ev.Event == "message" && !hasContent {
				var check struct {
					Type    string `json:"type"`
					Content string `json:"content"`
				}
				if json.Unmarshal([]byte(ev.Data), &check) == nil && check.Type == "text" && check.Content != "" {
					hasContent = true
				}
			}
			switch ev.Event {
			case "usage":
				var u usageData
				if json.Unmarshal([]byte(ev.Data), &u) == nil {
					usageChan <- &u
				}
			case "message":
				msgChan <- ev
			}
		}
		close(usageChan)
		lastMsgIDChan <- lastMsgID
		close(lastMsgIDChan)
		hasContentChan <- hasContent
		close(hasContentChan)
	}()

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		inThinking := false
		var buffered strings.Builder

		// Send message_start event
		startMsg := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":    fmt.Sprintf("msg_%s", uuid.New().String()[:24]),
				"type":  "message",
				"role":  "assistant",
				"model": routeResult.Model,
			},
		}
		fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("message_start", startMsg))
		// Send content_block_start for text block (index 0)
		textBlockStart := map[string]interface{}{
			"type":         "content_block_start",
			"index":        0,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		}
		fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("content_block_start", textBlockStart))
		flusher.Flush()

		for event := range msgChan {
			if event.Event != "message" {
				continue
			}
			var msg struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(event.Data), &msg); err != nil {
				continue
			}
			if msg.Type == "text" && msg.Content != "" {
				c := strings.ReplaceAll(msg.Content, "\u0000", "")
				c, inThinking = filterThinkingChunk(c, inThinking)
				if c != "" {
					buffered.WriteString(c)
					// Always stream text chunks immediately
					delta := adapter.AnthropicTextDelta{Type: "text_delta", Text: c}
					fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("content_block_delta", delta))
					flusher.Flush()
				}
			}
		}
		// Close text content block
		fmt.Fprintf(w, "event: content_block_stop\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0}))

		// Check for tool calls in buffered output
		finalText := strings.TrimSpace(buffered.String())
		if hasTools && toolcall.HasToolCallSyntax(finalText) {
			calls := toolcall.ParseToolCallsFromText(finalText)
			log.Printf("[tools] Anthropic stream: parsed %d calls from text", len(calls))
			if len(calls) > 0 {
				// Send tool_use blocks as streaming events
				for i, c := range calls {
					blockIdx := i + 1 // text block is index 0
					block := adapter.AnthropicToolUseBlock{
						Type:  "tool_use",
						ID:    "toolu_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24],
						Name:  c.Name,
						Input: c.Input,
					}
					toolStart := map[string]interface{}{
						"type":  "content_block_start",
						"index": blockIdx,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    block.ID,
							"name":  block.Name,
							"input": block.Input,
						},
					}
					fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("content_block_start", toolStart))
					fmt.Fprintf(w, "event: content_block_stop\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": blockIdx}))
				}
				fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("message_delta", map[string]interface{}{
					"type":        "message_delta",
					"stop_reason": "tool_use",
				}))
			}
		} else {
			fmt.Fprintf(w, "event: message_delta\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("message_delta", map[string]interface{}{
				"type":        "message_delta",
				"stop_reason": "end_turn",
			}))
		}
		fmt.Fprintf(w, "event: message_stop\ndata: %s\n\n", adapter.MakeAnthropicStreamEvent("message_stop", nil))
		flusher.Flush()
	} else {
		var content strings.Builder
		inThinking := false
		for event := range msgChan {
			if event.Event == "message" {
				var msg struct {
					Type    string `json:"type"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal([]byte(event.Data), &msg); err == nil && msg.Type == "text" {
					c := strings.ReplaceAll(msg.Content, "\u0000", "")
					c, inThinking = filterThinkingChunk(c, inThinking)
					content.WriteString(c)
				}
			}
		}
		finalText := strings.TrimSpace(content.String())
		// Check for tool calls in the response
		if hasTools && toolcall.HasToolCallSyntax(finalText) {
			calls := toolcall.ParseToolCallsFromText(finalText)
			log.Printf("[tools] Anthropic: parsed %d calls from text", len(calls))
			if len(calls) > 0 {
				// Return Anthropic format with tool_use blocks
				blocks := make([]interface{}, 0, len(calls))
				for _, c := range calls {
					blocks = append(blocks, adapter.AnthropicToolUseBlock{
						Type:  "tool_use",
						ID:    "toolu_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24],
						Name:  c.Name,
						Input: c.Input,
					})
				}
				resp := map[string]interface{}{
					"id":          fmt.Sprintf("msg_%s", uuid.New().String()[:24]),
					"type":        "message",
					"role":        "assistant",
					"content":     blocks,
					"model":       routeResult.Model,
					"stop_reason": "tool_use",
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			} else {
				resp := adapter.MakeAnthropicResponse(routeResult.Model, finalText)
				w.Header().Set("Content-Type", "application/json")
				w.Write(resp)
			}
		} else {
			resp := adapter.MakeAnthropicResponse(routeResult.Model, finalText)
			w.Header().Set("Content-Type", "application/json")
			w.Write(resp)
		}
	}

	// 记录 usage
	if u := <-usageChan; u != nil {
		cached := 0
		reasoning := 0
		if u.NativeUsage != nil {
			if u.NativeUsage.PromptDetails != nil {
				cached = u.NativeUsage.PromptDetails.CachedTokens
			}
			if u.NativeUsage.CompletionDetails != nil {
				reasoning = u.NativeUsage.CompletionDetails.ReasoningTokens
			}
		}
		stats.Get().Record(routeResult.Model, u.PromptTokens, u.CompletionTokens, cached, reasoning, u.TotalTokens)
	}

	// 更新 parentId + 同步网页端历史（仅在有实际内容时）
	hasContent := <-hasContentChan
	if lastMsgID := <-lastMsgIDChan; lastMsgID != "" && hasContent {
		h.convStore.SetParentID(key, lastMsgID)
		log.Printf("[conv] Anthropic: updated parentId for key=%s convID=%s: %s", key[:8], convID[:8], lastMsgID[:min(len(lastMsgID), 8)])
	} else if !hasContent {
		log.Printf("[conv] Anthropic: empty response, keeping previous parentId for key=%s convID=%s", key[:8], convID[:8])
	}
	// 保存对话到 MiMo 官网历史记录
	if hasContent {
		go client.SaveConversation(context.Background(), convID, query)
	}
}

// extractContentString extracts text content from OpenAI message Content field.
// Content can be a string or an array of content parts (multimodal format).
func extractContentString(content interface{}) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	// Handle []interface{} (from generic JSON decoding)
	if arr, ok := content.([]interface{}); ok {
		var buf strings.Builder
		for _, item := range arr {
			if m, ok := item.(map[string]interface{}); ok {
				typ, _ := m["type"].(string)
				if typ == "text" {
					if text, _ := m["text"].(string); text != "" {
						buf.WriteString(text)
					}
				}
			}
		}
		return buf.String()
	}
	// Handle []map[string]interface{} (some JSON decoders produce this)
	if arr, ok := content.([]map[string]interface{}); ok {
		var buf strings.Builder
		for _, m := range arr {
			typ, _ := m["type"].(string)
			if typ == "text" {
				if text, _ := m["text"].(string); text != "" {
					buf.WriteString(text)
				}
			}
		}
		return buf.String()
	}
	// Handle []adapter.ContentPart (strongly typed)
	if arr, ok := content.([]adapter.ContentPart); ok {
		var buf strings.Builder
		for _, part := range arr {
			if part.Type == "text" {
				buf.WriteString(part.Text)
			}
		}
		return buf.String()
	}
	// Fallback: try JSON marshal/unmarshal
	b, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	// Try to unmarshal as array of content parts
	var parts []adapter.ContentPart
	if json.Unmarshal(b, &parts) == nil {
		var buf strings.Builder
		for _, part := range parts {
			if part.Type == "text" {
				buf.WriteString(part.Text)
			}
		}
		return buf.String()
	}
	return ""
}

// extractFirstOpenAIUserMessage extracts the first user message from OpenAI messages.
func extractFirstOpenAIUserMessage(msgs []adapter.OpenAIMessage) string {
	for _, msg := range msgs {
		if msg.Role == "user" {
			if s := extractContentString(msg.Content); s != "" {
				return s
			}
		}
	}
	return ""
}

// extractLatestOpenAIUserMessage extracts the latest user message from OpenAI messages.
// Skips auto-generated messages like "predict next message" from MiMo Code.
func extractLatestOpenAIUserMessage(msgs []adapter.OpenAIMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			if s := extractContentString(msgs[i].Content); s != "" {
				if isAutoGeneratedQuery(s) {
					log.Printf("[filter] skipping auto-generated query (len=%d): %q", len(s), s[:min(len(s), 100)])
					continue
				}
				return s
			}
		}
	}
	return ""
}

// isAutoGeneratedQuery detects auto-generated messages from MiMo Code features
// like "predict next message" that should not be forwarded to MiMo.
func isAutoGeneratedQuery(s string) bool {
	lower := strings.ToLower(s)
	autoPatterns := []string{
		"based on the conversation above",
		"write the user's most likely next message",
		"most likely next message",
		"user's most likely",
		"above conversation",
	}
	for _, p := range autoPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Chinese patterns
	zhPatterns := []string{
		"根据以上对话",
		"根据对话上下文",
		"最可能发送的下一条消息",
		"预测用户的下一条消息",
		"写出用户最可能",
		"用户最可能的下一条",
		"用户最可能发送",
	}
	for _, p := range zhPatterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

