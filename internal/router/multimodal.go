package router

import (
	"strings"

	"github.com/zhangguoguo1314/mimo-free-api/internal/mimo"
)

const (
	ModelV25    = "mimo-v2.5"
	ModelV25Pro = "mimo-v2.5-pro"
)

// RouteResult 路由决策结果
type RouteResult struct {
	Model string // 实际要使用的模型
	Reason string // 路由原因
}

// RouteModel 根据消息内容决定使用哪个模型
// 规则：
// 1. 用户显式指定模型 → 尊重
// 2. 包含图片/音频/文件 → mimo-v2.5（全模态）
// 3. 纯文本 → mimo-v2.5-pro（推理最强）
func RouteModel(requestedModel string, messages []mimo.Message) RouteResult {
	// 如果用户指定了有效的模型，直接使用
	if requestedModel != "" && requestedModel != "auto" {
		normalized := normalizeModel(requestedModel)
		return RouteResult{Model: normalized, Reason: "explicit"}
	}

	// 检查消息内容
	for _, msg := range messages {
		if hasMultimodalContent(msg.Content) {
			return RouteResult{Model: ModelV25, Reason: "multimodal_content"}
		}
	}

	return RouteResult{Model: ModelV25Pro, Reason: "text_only"}
}

// hasMultimodalContent 检查内容是否包含非文本部分
func hasMultimodalContent(content interface{}) bool {
	switch v := content.(type) {
	case string:
		return false
	case []interface{}:
		for _, part := range v {
			if m, ok := part.(map[string]interface{}); ok {
				t, _ := m["type"].(string)
				if t == "image_url" || t == "audio" || t == "file" || t == "image" {
					return true
				}
			}
		}
	}
	return false
}

// normalizeModel 标准化模型名称
func normalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch m {
	case "mimo-v2.5", "mimo-v2.5-omni", "mimo-v2-omni":
		return ModelV25
	case "mimo-v2.5-pro", "mimo-v2-pro":
		return ModelV25Pro
	default:
		// 如果包含 omni 或 v2.5 但不带 pro，用 v2.5
		if strings.Contains(m, "omni") || (strings.Contains(m, "v2.5") && !strings.Contains(m, "pro")) {
			return ModelV25
		}
		if strings.Contains(m, "pro") {
			return ModelV25Pro
		}
		return ModelV25Pro // 默认
	}
}

// SupportedModels 返回支持的模型列表
func SupportedModels() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"id":       "mimo-v2.5",
			"object":   "model",
			"owned_by": "xiaomi",
			"capabilities": map[string]interface{}{
				"text":       true,
				"image":      true,
				"audio":      true,
				"file":       true,
				"reasoning":  true,
			},
		},
		{
			"id":       "mimo-v2.5-pro",
			"object":   "model",
			"owned_by": "xiaomi",
			"capabilities": map[string]interface{}{
				"text":       true,
				"image":      true,
				"file":       true,
				"reasoning":  true,
			},
		},
	}
}
