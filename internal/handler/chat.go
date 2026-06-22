package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// estimateTokens 基于文本内容估算 token 数量
// 中文字符约 1.5 tokens，英文约 0.75 tokens
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var tokens float64
	for _, r := range text {
		if r > 127 {
			// 非 ASCII 字符（中文、日文等）约 1.5 tokens
			tokens += 1.5
		} else if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			// 英文字母约 0.3 tokens（按词估算）
			tokens += 0.3
		} else if r >= '0' && r <= '9' {
			// 数字约 0.5 tokens
			tokens += 0.5
		} else {
			// 标点符号等约 0.25 tokens
			tokens += 0.25
		}
	}
	return int(tokens)
}

// estimateTokensFromCharLen 基于字符数快速估算 token 数量。
// 用于无法获取原始文本、只有字符数记录的场景（如流式输出）。
// 中文为主的场景约 1.2 tokens/char，英文约 0.25 tokens/char，取中间值。
func estimateTokensFromCharLen(charLen int) int {
	if charLen <= 0 {
		return 0
	}
	// 保守估算：平均约 0.8 tokens per character（混合中英文场景）
	return int(float64(charLen) * 0.8)
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

// handleWebChat 使用网页端反代 — 真正的流式模式
// 直接将 MiMo SSE 流转发为 OpenAI SSE 流，不缓冲整个响应
// 支持空回复自动重试：检测到空内容时切换账号重试，最多重试2次
func (h *ChatHandler) handleWebChat(ctx context.Context, w http.ResponseWriter, req *adapter.OpenAIChatRequest, model string, stream bool) {
	// Build query with full conversation history for context continuity.
	// This ensures context is preserved even when switching accounts.
	// Format: "user: xxx\nassistant: xxx\nuser: xxx" (latest message is the actual query)
	query := buildConversationQuery(req.Messages)
	if query == "" {
		log.Printf("[filter] all user messages auto-generated, returning empty response")
		writeError(w, http.StatusBadRequest, "no valid user message")
		return
	}

	// Extract images from the last user message for upload
	var multiMedias []mimo.MultiMedia
	mediaList := extractMediaFromMessages(req.Messages)
	if len(mediaList) > 0 {
		log.Printf("[media] found %d media items in request", len(mediaList))
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

		// Session binding: try to use the bound account for this conversation
		var client *mimo.WebClient
		var release func()
		var clientIdx int
		var err error
		var released bool

		boundIdx := h.convStore.GetAcctIdx(key)
		if boundIdx >= 0 {
			// Try to acquire the bound account specifically
			clientIdx, client, release, err = h.pool.AcquireSpecific(boundIdx)
			if err != nil {
				// Bound account unavailable, unbind and fall through to random selection
				log.Printf("[bind] bound account %d unavailable, unbinding", boundIdx)
				h.convStore.UnbindAcct(key)
			}
		}

		if client == nil {
			clientIdx, client, release, err = h.pool.AcquireIndex()
		}

		if err != nil {
			if attempt == maxRetries {
				writeError(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			continue
		}

		// Ensure release is always called, even on panic or blocked goroutines
		doRelease := func() {
			if !released {
				released = true
				release()
			}
		}
		defer doRelease()

		// Bind this conversation to the acquired account (if not already bound)
		if boundIdx < 0 && clientIdx >= 0 {
			h.convStore.SetAcctIdx(key, clientIdx)
			log.Printf("[bind] conversation %s bound to account %d", key[:8], clientIdx)
		}

		convID, parentID := h.convStore.GetOrCreate(key)

		// Upload media files if any (must be done after acquiring client for auth)
		if len(mediaList) > 0 && multiMedias == nil {
			log.Printf("[media] uploading %d media files...", len(mediaList))
			uploaded, err := uploadMediaFiles(ctx, client, mediaList)
			if err != nil {
				log.Printf("[media] upload failed: %v", err)
				// Return error to client so we know upload failed
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("media upload failed: %v", err))
				doRelease()
				return
			} else {
				multiMedias = uploaded
				log.Printf("[media] uploaded %d media files successfully", len(uploaded))
			}
		}

		stats.Get().IncrConcurrency()
		// Enable thinking so MiMo returns reasoning content
		body, err := client.Chat(ctx, query, model, convID, parentID, true, multiMedias)
		if err != nil {
			stats.Get().DecrConcurrency()
			doRelease()
			handleChatError(h.pool, client, err)
			// Unbind from failed account so next retry picks a new one
			h.convStore.UnbindAcct(key)
			if attempt == maxRetries {
				writeError(w, http.StatusBadGateway, fmt.Sprintf("mimo error: %v", err))
				return
			}
			continue
		}

		if stream {
			// True streaming: forward MiMo SSE events to OpenAI SSE in real-time
			eventsCh := make(chan mimo.WebSSEEvent, 100)
			parseErrCh := make(chan error, 1)
			go func() {
				parseErrCh <- mimo.ParseWebSSE(ctx, body, eventsCh)
			}()

			// Set up SSE headers immediately so 9Router sees first byte
			flusher, ok := w.(http.Flusher)
			if !ok {
				flusher = &noopFlusher{}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)

			// Send initial role chunk immediately so 9Router doesn't timeout
			initChunk := adapter.MakeOpenAIStreamChunk(model, "", false)
			// initChunk is already JSON []byte from MakeOpenAIStreamChunk, write directly
			fmt.Fprintf(w, "data: %s\n\n", initChunk)
			flusher.Flush()

			hasContent, lastMsgID, _ := h.streamWebToOpenAIWithThinking(w, model, eventsCh, len(req.Tools) > 0)

			stats.Get().DecrConcurrency()

			if parseErr := <-parseErrCh; parseErr != nil {
				log.Printf("[stream] parse error: %v", parseErr)
			}

			if hasContent {
				// Update parentID for conversation chaining (critical for context continuity)
				if lastMsgID != "" {
					h.convStore.SetParentID(key, lastMsgID)
				}
				// Save conversation in background
				go client.SaveConversation(context.Background(), convID, query)
				doRelease()
				return
			}

			doRelease()
			log.Printf("[retry] empty response from account, will retry")
			// Empty response: delete conversation state to get fresh context on retry
			h.convStore.Delete(key)
		} else {
			// Non-streaming: collect all content then respond
			respBody, err := io.ReadAll(body)
			body.Close()
			stats.Get().DecrConcurrency()
			if err != nil {
				doRelease()
				if attempt == maxRetries {
					writeError(w, http.StatusBadGateway, fmt.Sprintf("read response: %v", err))
					return
				}
				continue
			}

			content, errMsg := extractTextFromSSE(string(respBody))
			content = filterThinkingContent(content)

			if content != "" {
				h.writeNonStreamResponse(w, model, content, len(req.Tools) > 0)
				go client.SaveConversation(context.Background(), convID, query)
				doRelease()
				return
			}

			if errMsg != "" {
				log.Printf("[sse] attempt=%d got error from MiMo: %s, resetting conversation", attempt, errMsg)
				h.convStore.Delete(key)
			}

			doRelease()
			log.Printf("[retry] empty response from account, will retry")
		}
	}

	// All retries failed
	if stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			flusher = &noopFlusher{}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
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
		// Filter base64-encoded OpenAI chunks that MiMo echoes back
		c = filterThinkingContent(c)
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

// isBase64Fragment checks if a string looks like a base64-encoded JSON payload.
// These are OpenAI SSE chunks that MiMo client echoes back as messages.
func isBase64Fragment(s string) bool {
	// Must start with "eyJ" (base64 of '{"')
	if !strings.HasPrefix(s, "eyJ") {
		return false
	}
	// Check if it contains only base64 characters (alphanumeric, +, /, =)
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	// Must be at least 4 chars (minimum base64 block)
	return len(s) >= 4
}

// b64BufferState manages buffering of base64 fragments that may be split across
// multiple SSE chunks by MiMo's \x00 null byte separator.
type b64BufferState struct {
	buffer    strings.Builder
	isBuffering bool
}

// processPart handles a single part after \x00 splitting.
// It returns the cleaned content (empty string if the part should be skipped).
func (b *b64BufferState) processPart(part string) string {
	// If we're currently buffering a base64 fragment
	if b.isBuffering {
		// Check if this part continues the base64 or ends it
		if isBase64Fragment(part) {
			// Still base64, keep buffering
			b.buffer.WriteString(part)
			return ""
		}
		// The base64 sequence ended - discard the buffer and process this part normally
		b.isBuffering = false
		b.buffer.Reset()
		// Fall through to process this part below
	}

	// Check if this part is entirely a base64 sequence
	if isBase64Fragment(part) {
		b.buffer.WriteString(part)
		b.isBuffering = true
		return ""
	}

	// Handle mixed content: base64 embedded within normal text
	// e.g., "eyJ...base64...=你好！有什么我可以帮你的吗？"
	// Remove any base64 fragments from the middle of content
	cleaned := b64CleanRe.ReplaceAllString(part, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return ""
	}

	return cleaned
}

// flush returns any remaining buffered content (shouldn't normally happen)
func (b *b64BufferState) flush() string {
	if b.isBuffering && b.buffer.Len() > 0 {
		content := b.buffer.String()
		b.buffer.Reset()
		b.isBuffering = false
		return content
	}
	return ""
}

// Pre-compiled regex patterns for content filtering
var (
	b64CleanRe    = regexp.MustCompile(`eyJ[a-zA-Z0-9+/=]{4,}`)
	thinkBlockRe  = regexp.MustCompile(`(?s)<think\b[^>]*>.*?</think\s*>`)
	thinkTagRe    = regexp.MustCompile(`^<think[^>]*>`)
	endThinkTagRe = regexp.MustCompile(`</think\s*>`)
	mimoStatusRe  = regexp.MustCompile(`mimo returned (\d+)`)
)

// filterThinkingContent 从完整文本中移除 <think...>...</think...> 标签及其内容
func filterThinkingContent(content string) string {
	// 移除 \u0000 字符
	content = strings.ReplaceAll(content, "\u0000", "")

	// 使用正则表达式移除 <think...>...</think...> 块
	content = thinkBlockRe.ReplaceAllString(content, "")

	// 移除 [DONE] 标记
	content = strings.TrimSuffix(content, "[DONE]")
	content = strings.TrimSpace(content)

	// Remove base64-encoded OpenAI request/response bodies that MiMo echoes back.
	// Use lower threshold of 4 chars to catch fragments, but require eyJ prefix
	content = b64CleanRe.ReplaceAllString(content, "")

	content = strings.TrimSpace(content)
	return content
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

// writeStreamResponse 将已收集的文本作为 SSE 流写入响应
func (h *ChatHandler) writeStreamResponse(w http.ResponseWriter, model string, text string, hasTools bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		flusher = &noopFlusher{}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// 检测是否包含工具调用
	if hasTools && len(text) > 0 {
		if toolcall.HasToolCallSyntax(text) {
			calls := toolcall.ParseToolCallsFromText(text)
			if len(calls) > 0 {
				toolCalls := toolcall.ConvertToolCallsToOpenAI(calls)
				finishChunk := adapter.MakeOpenAIStreamChunk(model, "", true)
				fmt.Fprintf(w, "data: %s\n\n", finishChunk)
				toolChunk := adapter.MakeOpenAIStreamToolCallChunk(model, toolCalls, true)
				fmt.Fprintf(w, "data: %s\n\n", toolChunk)
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
		}
	}

	// 按 rune 分块发送（避免截断多字节 UTF-8 字符）
	chunkSize := 50
	runes := []rune(text)
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunk := adapter.MakeOpenAIStreamChunk(model, string(runes[i:end]), false)
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
	}

	finishChunk := adapter.MakeOpenAIStreamChunk(model, "", true)
	fmt.Fprintf(w, "data: %s\n\n", finishChunk)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// writeNonStreamResponse 将已收集的文本作为非流式响应写入
func (h *ChatHandler) writeNonStreamResponse(w http.ResponseWriter, model string, text string, hasTools bool) {
	// 检测是否包含工具调用
	if hasTools && len(text) > 0 {
		if toolcall.HasToolCallSyntax(text) {
			calls := toolcall.ParseToolCallsFromText(text)
			if len(calls) > 0 {
				toolCalls := toolcall.ConvertToolCallsToOpenAI(calls)
				resp := adapter.MakeOpenAIToolCallResponse(model, toolCalls)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(resp)
				return
			}
		}
	}

	resp := adapter.MakeOpenAIResponse(model, text)
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

// streamWebToOpenAIWithThinking forwards MiMo SSE events to OpenAI SSE format in real-time.
// MiMo SSE data format: {"type":"text","content":"..."} or {"content":"[DONE]"} or {"content":"msgId"}
// Text content may contain <think...>\x00...thinking...\x00</think...\x00actual response
// Thinking content is sent as reasoning_content, actual response as content.
// Returns (hasContent, lastMsgID, usage).
func (h *ChatHandler) streamWebToOpenAIWithThinking(w http.ResponseWriter, model string, events <-chan mimo.WebSSEEvent, hasTools bool) (bool, string, adapter.OpenAIUsage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		flusher = &noopFlusher{}
	}

	var hasContent bool
	var totalContentLen int
	var totalThinkingLen int
	var contentBuf strings.Builder  // Accumulate raw content for accurate token estimation
	var thinkingBuf strings.Builder // Accumulate raw thinking for accurate token estimation
	var lastMsgID string
	var usage adapter.OpenAIUsage

	// State machine for thinking detection within content
	inThinking := false

	// Base64 buffer state machine to catch base64 fragments split across \x00 boundaries
	var b64Buf b64BufferState
	defer func() {
		// Log any remaining buffered base64 that wasn't flushed
		if leftover := b64Buf.flush(); leftover != "" {
			log.Printf("[stream] b64 buffer leftover (len=%d): %q", len(leftover), leftover[:min(len(leftover), 100)])
		}
	}()

	for event := range events {
		if event.Data == "" {
			continue
		}

		// Track lastMsgID for conversation chaining
		if event.ID != "" {
			lastMsgID = event.ID
		}

		// Parse MiMo JSON data format
		var parsed struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(event.Data), &parsed); err != nil {
			// Not JSON, treat as raw text (fallback)
			parsed.Content = event.Data
		}

		// Skip non-text events (message IDs, usage, etc.)
		if parsed.Type != "text" && parsed.Type != "" {
			// Check for [DONE] marker
			if strings.Contains(parsed.Content, "[DONE]") {
				break
			}
			// Extract usage data from MiMo
			if strings.Contains(event.Data, "promptTokens") || strings.Contains(event.Data, "completionTokens") {
				var u struct {
					PromptTokens     int `json:"promptTokens"`
					CompletionTokens int `json:"completionTokens"`
				}
				if json.Unmarshal([]byte(event.Data), &u) == nil {
					usage.PromptTokens = u.PromptTokens
					usage.CompletionTokens = u.CompletionTokens
					usage.TotalTokens = u.PromptTokens + u.CompletionTokens
				}
			}
			continue
		}

		// Skip events that look like message IDs (short numeric content without type)
		if parsed.Type == "" && parsed.Content != "" {
			// This is likely a message ID (e.g., {"content":"4958117"})
			// Track it as lastMsgID
			lastMsgID = parsed.Content
			continue
		}

		content := parsed.Content
		if content == "" {
			continue
		}

		// Split content by null bytes (\x00) - MiMo uses null as separator
		// Format: <think...>\x00thinking_text\x00</think\x00actual_response
		parts := strings.Split(content, "\x00")
		for _, part := range parts {
			if part == "" {
				continue
			}

			// Run through base64 buffer state machine first
			part = b64Buf.processPart(part)
			if part == "" {
				continue
			}

			// Detect thinking start
			if strings.HasPrefix(part, "<think") && !strings.Contains(part, "</think") {
				inThinking = true
				// Extract content after <think...> tag
				after := thinkTagRe.ReplaceAllString(part, "")
				if after != "" {
					thinkingBuf.WriteString(after)
					chunk := adapter.MakeOpenAIStreamThinkingChunk(model, after)
					fmt.Fprintf(w, "data: %s\n\n", chunk)
					flusher.Flush()
					totalThinkingLen += len(after)
					hasContent = true
				}
				continue
			}

			// Detect thinking end (may have content after </think...>)
			if strings.Contains(part, "</think") {
				beforeThink := endThinkTagRe.ReplaceAllString(part, "")
				if beforeThink != "" {
					thinkingBuf.WriteString(beforeThink)
					chunk := adapter.MakeOpenAIStreamThinkingChunk(model, beforeThink)
					fmt.Fprintf(w, "data: %s\n\n", chunk)
					flusher.Flush()
					totalThinkingLen += len(beforeThink)
					hasContent = true
				}
				inThinking = false
				continue
			}

			if inThinking {
				thinkingBuf.WriteString(part)
				chunk := adapter.MakeOpenAIStreamThinkingChunk(model, part)
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
				totalThinkingLen += len(part)
				hasContent = true
				continue
			}

			// Regular content - filter any remaining thinking tags
			cleaned := filterThinkingContent(part)
			if cleaned == "" {
				continue
			}

			// Check for tool calls
			if hasTools && toolcall.HasToolCallSyntax(cleaned) {
				contentBuf.WriteString(cleaned)
				chunk := adapter.MakeOpenAIStreamChunk(model, cleaned, false)
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
				totalContentLen += len(cleaned)
				hasContent = true
				continue
			}

			contentBuf.WriteString(cleaned)
			chunk := adapter.MakeOpenAIStreamChunk(model, cleaned, false)
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
			totalContentLen += len(cleaned)
			hasContent = true
		}
	}

	// Send finish chunk
	finishChunk := adapter.MakeOpenAIStreamChunk(model, "", true)
	fmt.Fprintf(w, "data: %s\n\n", finishChunk)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	// Record stats with actual usage from MiMo, fallback to estimation
	if usage.TotalTokens == 0 {
		// Use accumulated raw text for accurate estimation
		contentText := contentBuf.String()
		thinkingText := thinkingBuf.String()
		usage.PromptTokens = estimateTokens(contentText) + estimateTokens(thinkingText)
		usage.CompletionTokens = estimateTokens(contentText) + estimateTokens(thinkingText)
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	stats.Get().Record(model, usage.PromptTokens, usage.CompletionTokens, 0, totalThinkingLen, usage.TotalTokens)

	log.Printf("[stream] done: contentLen=%d thinkingLen=%d lastMsgID=%s usage=%+v", totalContentLen, totalThinkingLen, lastMsgID, usage)

	return hasContent, lastMsgID, usage
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

	body, err := client.Chat(ctx, query, routeResult.Model, convID, parentID, false, nil)
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
				// Filter base64-encoded OpenAI chunks that MiMo echoes back
				c = filterThinkingContent(c)
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
					// Filter base64-encoded OpenAI chunks that MiMo echoes back
					c = filterThinkingContent(c)
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

// buildConversationQuery constructs a query string from the full conversation history.
// This ensures context is preserved even when switching accounts.
// Format: each message is prefixed with "user: " or "assistant: ".
// Only the last user message is the actual query; previous messages provide context.
// buildConversationQuery builds the query string for MiMo from OpenAI messages.
// Uses a sliding window strategy to avoid "text too long" errors:
//   - Takes the last N rounds of conversation (up to maxQueryChars)
//   - MiMo maintains full server-side context via conversationId + parentId
//   - The sliding window provides fallback context when switching accounts
const (
	maxQueryRounds  = 3  // max conversation rounds to include (1 round = user + assistant)
	maxQueryChars   = 4000 // max total query length in characters
)

func buildConversationQuery(msgs []adapter.OpenAIMessage) string {
	// Step 1: Filter valid messages (skip system, auto-generated, encoded data, etc.)
	type msgEntry struct {
		role    string
		content string
	}
	var valid []msgEntry
	for _, msg := range msgs {
		if msg.Role == "system" {
			continue
		}
		content := extractContentString(msg.Content)
		if content == "" {
			continue
		}
		if msg.Role == "user" && isAutoGeneratedQuery(content) {
			continue
		}
		if strings.HasPrefix(content, "eyJ") && (strings.Contains(content, "chatcmpl") || strings.Contains(content, "chat.completion") || len(content) > 200) {
			continue
		}
		if strings.Contains(content, `"object":"chat.completion`) {
			continue
		}
		if strings.HasPrefix(content, `{"id":"chatcmpl`) || strings.HasPrefix(content, `{"id":"chatcmp`) {
			continue
		}
		if len(content) > 500 && !strings.Contains(content, " ") && !strings.Contains(content, "\n") {
			continue
		}
		valid = append(valid, msgEntry{role: msg.Role, content: content})
	}

	if len(valid) == 0 {
		return ""
	}

	// Step 2: Build from the tail, collecting up to maxQueryRounds rounds (user+assistant pairs)
	// Always include the latest message even if it exceeds the round limit.
	var parts []string
	roundCount := 0
	for i := len(valid) - 1; i >= 0; i-- {
		entry := valid[i]
		prefix := entry.role + ": "
		part := prefix + entry.content + "\n"

		// Count rounds: a "user" message that is NOT the last message starts a new round
		if entry.role == "user" && i < len(valid)-1 {
			roundCount++
			if roundCount > maxQueryRounds {
				break
			}
		}

		parts = append(parts, part)
	}

	// Step 3: Reverse to get correct order and check total length
	var sb strings.Builder
	totalLen := 0
	for i := len(parts) - 1; i >= 0; i-- {
		totalLen += len(parts[i])
		if totalLen > maxQueryChars {
			// If adding this part would exceed the limit, skip it
			// (but always keep at least the latest message)
			if i == len(parts)-1 {
				// Latest message is too long, truncate it
				remaining := maxQueryChars - sb.Len()
				if remaining > 0 {
					// Find the content portion (after "user: " or "assistant: ")
					text := parts[i]
					prefixLen := strings.IndexByte(text, ' ') + 1
					if prefixLen > len(text) {
						prefixLen = len(text)
					}
					sb.WriteString(text[:prefixLen])
					sb.WriteString(text[prefixLen+1 : prefixLen+1+min(remaining-prefixLen, len(text)-prefixLen-1)])
					sb.WriteString("\n")
				}
			}
			continue
		}
		sb.WriteString(parts[i])
	}

	return sb.String()
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

// pendingMedia represents a media file extracted from an OpenAI message
// that needs to be uploaded to MiMo's storage before sending the chat request.
type pendingMedia struct {
	Data      []byte // Raw file data (decoded from base64)
	MediaType string // e.g., "image/png"
	FileName  string // e.g., "upload_123456.png"
	URL       string // If already a URL (not base64), use directly
}

// extractMediaFromMessages extracts media files (images, etc.) from OpenAI format messages.
// It looks at the last user message's content parts for type=="image_url" entries.
func extractMediaFromMessages(msgs []adapter.OpenAIMessage) []pendingMedia {
	var media []pendingMedia

	// Find the last user message
	var lastUser *adapter.OpenAIMessage
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = &msgs[i]
			break
		}
	}
	if lastUser == nil {
		log.Printf("[media] no user message found")
		return nil
	}

	log.Printf("[media] last user content type: %T, value: %v", lastUser.Content, lastUser.Content)

	// Extract content parts
	content := lastUser.Content
	var parts []adapter.ContentPart

	switch v := content.(type) {
	case string:
		// Plain text, no media
		return nil
	case []adapter.ContentPart:
		parts = v
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				part := adapter.ContentPart{}
				if typ, _ := m["type"].(string); typ == "image_url" {
					part.Type = "image_url"
					if iu, ok := m["image_url"].(map[string]interface{}); ok {
						if u, _ := iu["url"].(string); u != "" {
							part.ImageURL = &struct {
								URL    string `json:"url"`
								Detail string `json:"detail,omitempty"`
							}{URL: u}
						}
					}
				} else if typ == "text" {
					part.Type = "text"
					if t, _ := m["text"].(string); t != "" {
						part.Text = t
					}
				}
				parts = append(parts, part)
			}
		}
	case []map[string]interface{}:
		for _, m := range v {
			part := adapter.ContentPart{}
			if typ, _ := m["type"].(string); typ == "image_url" {
				part.Type = "image_url"
				if iu, ok := m["image_url"].(map[string]interface{}); ok {
					if u, _ := iu["url"].(string); u != "" {
						part.ImageURL = &struct {
							URL    string `json:"url"`
							Detail string `json:"detail,omitempty"`
						}{URL: u}
					}
				}
			} else if typ == "text" {
				part.Type = "text"
				if t, _ := m["text"].(string); t != "" {
					part.Text = t
				}
			}
			parts = append(parts, part)
		}
	default:
		// Try JSON marshal/unmarshal
		b, err := json.Marshal(content)
		if err != nil {
			return nil
		}
		if json.Unmarshal(b, &parts) == nil {
			// ok
		} else {
			return nil
		}
	}

	// Process parts
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil {
			rawURL := part.ImageURL.URL
			if strings.HasPrefix(rawURL, "data:") {
				// Base64 encoded image
				mediaType, data, fileName, err := mimo.ParseDataURI(rawURL)
				if err != nil {
					log.Printf("[media] failed to parse data URI: %v", err)
					continue
				}
				// Map MIME type to MiMo's MediaTypeEnum: image, file, audio, video, media
				mimoType := "image"
				if strings.HasPrefix(mediaType, "video/") {
					mimoType = "video"
				} else if strings.HasPrefix(mediaType, "audio/") {
					mimoType = "audio"
				} else if mediaType == "application/pdf" {
					mimoType = "file"
				}
				media = append(media, pendingMedia{
					Data:      data,
					MediaType: mimoType,
					FileName:  fileName,
				})
			} else if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
				// URL-based image - pass through directly
				media = append(media, pendingMedia{
					MediaType: "image",
					FileName:  "image.jpg",
					URL:       rawURL,
				})
			}
		}
	}

	return media
}

// uploadMediaFiles uploads pending media files to MiMo's storage using the provided client.
func uploadMediaFiles(ctx context.Context, client *mimo.WebClient, pending []pendingMedia) ([]mimo.MultiMedia, error) {
	var result []mimo.MultiMedia
	var errs []string

	for _, m := range pending {
		if m.URL != "" {
			// Already a URL, no upload needed
			result = append(result, mimo.MultiMedia{
				MediaType: m.MediaType,
				Name:      m.FileName,
				URL:       m.URL,
				FileURL:   m.URL,
				Status:    "completed",
			})
			continue
		}

		// Upload base64 data
		uploaded, err := client.UploadMedia(ctx, m.Data, m.FileName, m.MediaType)
		if err != nil {
			errMsg := fmt.Sprintf("upload %s: %v", m.FileName, err)
			log.Printf("[media] %s", errMsg)
			errs = append(errs, errMsg)
			continue
		}
		result = append(result, *uploaded)
	}

	if len(result) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("all uploads failed: %s", strings.Join(errs, "; "))
	}

	return result, nil
}

