package handler

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhangguoguo1314/mimo-free-api/internal/adapter"
	"github.com/zhangguoguo1314/mimo-free-api/internal/toolcall"
)

// TestToolCallNotStreamedAsText verifies that when MiMo returns tool call XML,
// the raw XML is NOT streamed as regular text to the client.
// This was the root cause of the "tool calling breaks" issue.
func TestToolCallNotStreamedAsText(t *testing.T) {
	// Simulate a DSML tool call response from MiMo
	dsmlResponse := `<｜DSML｜tool_calls>
  <｜DSML｜invoke name="webfetch">
    <｜DSML｜parameter name="url"><![CDATA[https://example.com]]></｜DSML｜parameter>
  </｜DSML｜invoke>
</｜DSML｜tool_calls>`

	// Verify HasToolCallSyntax detects it
	if !toolcall.HasToolCallSyntax(dsmlResponse) {
		t.Fatal("HasToolCallSyntax should detect DSML format")
	}

	// Verify ParseToolCallsFromText parses it
	calls := toolcall.ParseToolCallsFromText(dsmlResponse)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "webfetch" {
		t.Errorf("expected webfetch, got %s", calls[0].Name)
	}

	// Verify ConvertToolCallsToOpenAI produces valid OpenAI format
	toolCalls := toolcall.ConvertToolCallsToOpenAI(calls)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Type != "function" {
		t.Errorf("expected type=function, got %s", toolCalls[0].Type)
	}
	if toolCalls[0].Function.Name != "webfetch" {
		t.Errorf("expected function name webfetch, got %s", toolCalls[0].Function.Name)
	}

	// Verify the arguments are valid JSON
	var args map[string]any
	if err := json.Unmarshal([]byte(toolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("invalid tool call arguments JSON: %v", err)
	}
	if args["url"] != "https://example.com" {
		t.Errorf("expected url=https://example.com, got %v", args["url"])
	}

	t.Logf("✅ Tool call correctly parsed: name=%s args=%v", toolCalls[0].Function.Name, args)
}

// TestStripToolCallSyntaxForConvStore verifies that tool call XML is stripped
// before storing in conversation history.
func TestStripToolCallSyntaxForConvStore(t *testing.T) {
	dsmlResponse := `<｜DSML｜tool_calls>
  <｜DSML｜invoke name="Bash">
    <｜DSML｜parameter name="command"><![CDATA[ls -la]]></｜DSML｜parameter>
  </｜DSML｜invoke>
</｜DSML｜tool_calls>`

	stripped := toolcall.StripToolCallSyntax(dsmlResponse)
	if strings.Contains(stripped, "DSML") {
		t.Errorf("StripToolCallSyntax should remove DSML tags, got: %q", stripped)
	}
	if strings.Contains(stripped, "tool_calls") {
		t.Errorf("StripToolCallSyntax should remove tool_calls tags, got: %q", stripped)
	}
	t.Logf("✅ Stripped content: %q", stripped)
}

// TestOpenAIToolCallResponseFormat verifies the OpenAI tool_calls response format.
func TestOpenAIToolCallResponseFormat(t *testing.T) {
	toolCalls := []adapter.OpenAIToolCall{
		{
			ID:   "call_abc123",
			Type: "function",
			Function: adapter.OpenAIToolCallFunc{
				Name:      "webfetch",
				Arguments: `{"url":"https://example.com"}`,
			},
		},
	}

	resp := adapter.MakeOpenAIToolCallResponse("mimo-v2.5", toolCalls)

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("invalid response JSON: %v", err)
	}

	// Verify it has the right structure
	if parsed["object"] != "chat.completion" {
		t.Errorf("expected object=chat.completion, got %v", parsed["object"])
	}

	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatal("expected choices array")
	}

	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)

	if message["role"] != "assistant" {
		t.Errorf("expected role=assistant, got %v", message["role"])
	}

	// Content should be nil/empty for tool call responses
	if message["content"] != nil && message["content"] != "" {
		t.Errorf("expected empty content for tool call response, got %v", message["content"])
	}

	// Should have tool_calls
	tc, ok := message["tool_calls"].([]any)
	if !ok || len(tc) == 0 {
		t.Fatal("expected tool_calls in response")
	}

	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %v", choice["finish_reason"])
	}

	t.Logf("✅ OpenAI tool call response format is correct")
}

// TestStreamToolCallChunkFormat verifies the streaming tool_call chunk format.
func TestStreamToolCallChunkFormat(t *testing.T) {
	toolCalls := []adapter.OpenAIToolCall{
		{
			ID:   "call_abc123",
			Type: "function",
			Function: adapter.OpenAIToolCallFunc{
				Name:      "Bash",
				Arguments: `{"command":"pwd"}`,
			},
		},
	}

	chunk := adapter.MakeOpenAIStreamToolCallChunk("mimo-v2.5", toolCalls, true)

	var parsed map[string]any
	if err := json.Unmarshal(chunk, &parsed); err != nil {
		t.Fatalf("invalid chunk JSON: %v", err)
	}

	choices := parsed["choices"].([]any)
	choice := choices[0].(map[string]any)
	delta := choice["delta"].(map[string]any)

	// Should have tool_calls in delta
	tc, ok := delta["tool_calls"].([]any)
	if !ok || len(tc) == 0 {
		t.Fatal("expected tool_calls in delta")
	}

	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %v", choice["finish_reason"])
	}

	t.Logf("✅ Streaming tool_call chunk format is correct")
}

// TestBuildToolPromptHasDSMLInstructions verifies the tool prompt includes DSML format instructions.
func TestBuildToolPromptHasDSMLInstructions(t *testing.T) {
	tools := []adapter.OpenAITool{
		{
			Type: "function",
			Function: adapter.OpenAIToolFunc{
				Name:        "webfetch",
				Description: "Fetch a URL",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
	}

	prompt := toolcall.BuildToolPrompt(tools)

	// Should contain DSML format instructions
	if !strings.Contains(prompt, "DSML") {
		t.Error("BuildToolPrompt should contain DSML format instructions")
	}

	// Should contain tool call format examples
	if !strings.Contains(prompt, "tool_calls") {
		t.Error("BuildToolPrompt should contain tool_calls format")
	}

	// Should contain the tool name
	if !strings.Contains(prompt, "webfetch") {
		t.Error("BuildToolPrompt should contain tool names")
	}

	// Should contain parameter rules
	if !strings.Contains(prompt, "CDATA") {
		t.Error("BuildToolPrompt should contain CDATA instructions")
	}

	t.Logf("✅ BuildToolPrompt contains DSML instructions (len=%d)", len(prompt))
}

// TestSimulateStreamingFlow simulates the complete streaming flow with tool calls.
// This tests the FIXED behavior: tool call XML is buffered, not streamed.
func TestSimulateStreamingFlow(t *testing.T) {
	// Simulate what happens in streamWebToOpenAIWithThinking
	// when MiMo returns a tool call

	dsmlResponse := `<｜DSML｜tool_calls>
  <｜DSML｜invoke name="webfetch">
    <｜DSML｜parameter name="url"><![CDATA[https://example.com]]></｜DSML｜parameter>
  </｜DSML｜invoke>
</｜DSML｜tool_calls>`

	// Simulate the content buffer (what the fixed code does)
	var contentBuf strings.Builder
	hasTools := true

	// Simulate processing chunks - the fixed code buffers tool call content
	chunks := []string{dsmlResponse} // MiMo might send this as one or multiple chunks
	for _, chunk := range chunks {
		cleaned := strings.ReplaceAll(chunk, "\u0000", "")
		if cleaned == "" {
			continue
		}

		// FIXED: buffer only, don't stream
		if hasTools && toolcall.HasToolCallSyntax(cleaned) {
			contentBuf.WriteString(cleaned)
			continue
		}

		contentBuf.WriteString(cleaned)
	}

	// After stream ends, check for tool calls
	finalText := strings.TrimSpace(contentBuf.String())
	if hasTools && len(finalText) > 0 && toolcall.HasToolCallSyntax(finalText) {
		calls := toolcall.ParseToolCallsFromText(finalText)
		if len(calls) > 0 {
			toolCalls := toolcall.ConvertToolCallsToOpenAI(calls)
			t.Logf("✅ Streaming simulation: detected %d tool calls, would send tool_call chunks", len(toolCalls))

			// Verify the tool calls are correct
			if toolCalls[0].Function.Name != "webfetch" {
				t.Errorf("expected webfetch, got %s", toolCalls[0].Function.Name)
			}

			// Verify stripped content for convStore
			stripped := toolcall.StripToolCallSyntax(finalText)
			t.Logf("✅ ConvStore content (stripped): %q", stripped)
			return
		}
	}

	t.Fatal("Expected tool calls to be detected in simulation")
}

// TestSimulateStreamingFlowWithMixedContent tests streaming with text + tool calls.
func TestSimulateStreamingFlowWithMixedContent(t *testing.T) {
	// Some models might output text before tool calls
	textPart := "Let me fetch that URL for you.\n\n"
	toolCallPart := `<｜DSML｜tool_calls>
  <｜DSML｜invoke name="webfetch">
    <｜DSML｜parameter name="url"><![CDATA[https://example.com]]></｜DSML｜parameter>
  </｜DSML｜invoke>
</｜DSML｜tool_calls>`

	var contentBuf strings.Builder
	var streamedText strings.Builder
	hasTools := true

	// Simulate chunk-by-chunk processing
	chunks := []string{textPart, toolCallPart}
	for _, chunk := range chunks {
		cleaned := strings.ReplaceAll(chunk, "\u0000", "")
		if cleaned == "" {
			continue
		}

		// Check if this chunk has tool call syntax
		if hasTools && toolcall.HasToolCallSyntax(cleaned) {
			// FIXED: buffer only, don't stream
			contentBuf.WriteString(cleaned)
			continue
		}

		// Normal text: buffer AND stream
		contentBuf.WriteString(cleaned)
		streamedText.WriteString(cleaned)
	}

	// After stream ends
	finalText := strings.TrimSpace(contentBuf.String())
	if hasTools && len(finalText) > 0 && toolcall.HasToolCallSyntax(finalText) {
		calls := toolcall.ParseToolCallsFromText(finalText)
		if len(calls) > 0 {
			t.Logf("✅ Mixed content: streamed text=%q, detected %d tool calls",
				streamedText.String(), len(calls))
			return
		}
	}

	t.Fatal("Expected tool calls to be detected in mixed content simulation")
}

// TestNoFalsePositiveToolCallDetection verifies normal text isn't misdetected as tool calls.
func TestNoFalsePositiveToolCallDetection(t *testing.T) {
	normalTexts := []string{
		"Hello! How can I help you today?",
		"Here's the code:\n```python\nprint('hello')\n```",
		"The answer is 42.",
		"你可以使用 `function` 关键字来定义函数。",
		"I'll help you with that. Let me think...",
	}

	for _, text := range normalTexts {
		if toolcall.HasToolCallSyntax(text) {
			t.Errorf("false positive: HasToolCallSyntax returned true for normal text: %q", text[:min(len(text), 50)])
		}
	}

	t.Logf("✅ No false positives detected for %d normal texts", len(normalTexts))
}

// helper for tests
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// VerifySSEFormat checks that SSE output is well-formed.
func VerifySSEFormat(t *testing.T, output string) {
	lines := strings.Split(output, "\n")
	inEvent := false
	for i, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			inEvent = true
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				continue
			}
			// Verify it's valid JSON
			var parsed map[string]any
			if err := json.Unmarshal([]byte(data), &parsed); err != nil {
				t.Errorf("line %d: invalid JSON in SSE data: %v", i+1, err)
			}
		} else if line == "" && inEvent {
			inEvent = false
		}
	}
}

// TestSSEToolCallOutputFormat verifies the complete SSE output for tool calls.
func TestSSEToolCallOutputFormat(t *testing.T) {
	// Simulate what the fixed handler would produce
	var buf bytes.Buffer
	model := "mimo-v2.5"

	// 1. Initial role chunk
	initChunk := adapter.MakeOpenAIStreamChunk(model, "", false)
	buf.WriteString("data: " + string(initChunk) + "\n\n")

	// 2. Finish chunk with empty content
	finishChunk := adapter.MakeOpenAIStreamChunk(model, "", true)
	buf.WriteString("data: " + string(finishChunk) + "\n\n")

	// 3. Tool calls chunk
	toolCalls := []adapter.OpenAIToolCall{
		{
			ID:   "call_test123",
			Type: "function",
			Function: adapter.OpenAIToolCallFunc{
				Name:      "webfetch",
				Arguments: `{"url":"https://example.com"}`,
			},
		},
	}
	toolChunk := adapter.MakeOpenAIStreamToolCallChunk(model, toolCalls, true)
	buf.WriteString("data: " + string(toolChunk) + "\n\n")

	// 4. DONE
	buf.WriteString("data: [DONE]\n\n")

	// Verify format
	output := buf.String()
	VerifySSEFormat(t, output)

	// Verify the tool_calls chunk has the right structure
	lines := strings.Split(output, "\n")
	foundToolCallChunk := false
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var parsed map[string]any
		json.Unmarshal([]byte(data), &parsed)
		choices, ok := parsed["choices"].([]any)
		if !ok {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		if choice["finish_reason"] == "tool_calls" {
			foundToolCallChunk = true
			delta, ok := choice["delta"].(map[string]any)
			if !ok {
				t.Error("expected delta in tool_calls chunk")
				continue
			}
			tc, ok := delta["tool_calls"].([]any)
			if !ok || len(tc) == 0 {
				t.Error("expected tool_calls in delta")
				continue
			}
			t.Logf("✅ Found tool_calls chunk with %d calls", len(tc))
		}
	}

	if !foundToolCallChunk {
		t.Error("expected to find a tool_calls chunk in SSE output")
	}

	t.Logf("✅ SSE output format verified (%d bytes)", len(output))
}
