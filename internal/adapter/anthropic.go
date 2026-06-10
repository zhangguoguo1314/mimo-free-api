package adapter

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AnthropicRequest 是 Anthropic Messages API 格式
type AnthropicRequest struct {
	Model       string              `json:"model"`
	Messages    []AnthropicMessage  `json:"messages"`
	System      string              `json:"system,omitempty"`
	MaxTokens   int                 `json:"max_tokens"`
	Stream      bool                `json:"stream"`
	Temperature *float64            `json:"temperature,omitempty"`
}

// AnthropicMessage 是 Anthropic 格式的消息
type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// AnthropicResponse 是 Anthropic 格式的响应
type AnthropicResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Content      []AnthropicBlock     `json:"content"`
	Model        string               `json:"model"`
	StopReason   string               `json:"stop_reason"`
	Usage        AnthropicUsage       `json:"usage"`
}

// AnthropicBlock 是内容块
type AnthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AnthropicUsage 是 token 使用量
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicStreamEvent 是流式事件
type AnthropicStreamEvent struct {
	Type  string      `json:"type"`
	Index int         `json:"index,omitempty"`
	Delta interface{} `json:"delta,omitempty"`
}

// AnthropicTextDelta 是文本增量
type AnthropicTextDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}


func MakeAnthropicResponse(model, content string) []byte {
	resp := AnthropicResponse{
		ID:      fmt.Sprintf("msg_%s", uuid.New().String()[:24]),
		Type:    "message",
		Role:    "assistant",
		Content: []AnthropicBlock{{Type: "text", Text: content}},
		Model:   model,
		StopReason: "end_turn",
	}
	data, _ := json.Marshal(resp)
	return data
}

// MakeAnthropicStreamEvent 创建 Anthropic 流式事件
func MakeAnthropicStreamEvent(eventType string, data interface{}) []byte {
	now := time.Now().Unix()
	event := map[string]interface{}{
		"type":    eventType,
		"created": now,
	}
	if data != nil {
		event["data"] = data
	}
	b, _ := json.Marshal(event)
	return b
}
