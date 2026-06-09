package services

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qingdeng888/qwen2API/internal/toolcall"
	"github.com/qingdeng888/qwen2API/internal/upstream"
)

// OpenAI Stream Translator
type OpenAITranslator struct {
	Model     string
	RequestID string
	Thinking  strings.Builder
	Answer    strings.Builder
}

func NewOpenAITranslator(model string) *OpenAITranslator {
	return &OpenAITranslator{Model: model, RequestID: "chatcmpl-" + NewUUID()[:12]}
}

func (t *OpenAITranslator) TranslateEvent(evt *upstream.StreamEvent) string {
	if evt == nil || evt.Type != "delta" || evt.Content == "" {
		return ""
	}
	var delta map[string]interface{}
	if evt.Phase == "thinking_summary" || evt.Phase == "thinking" {
		t.Thinking.WriteString(evt.Content)
		delta = map[string]interface{}{"reasoning_content": evt.Content}
	} else {
		t.Answer.WriteString(evt.Content)
		delta = map[string]interface{}{"content": evt.Content}
	}
	return t.chunk(delta, nil)
}

func (t *OpenAITranslator) FinishChunk() string {
	fr := "stop"
	return t.chunk(map[string]interface{}{}, &fr)
}

func (t *OpenAITranslator) DoneChunk() string { return "data: [DONE]\n\n" }

func (t *OpenAITranslator) chunk(delta map[string]interface{}, finish *string) string {
	c := map[string]interface{}{
		"id": t.RequestID, "object": "chat.completion.chunk",
		"created": time.Now().Unix(), "model": t.Model,
		"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	d, _ := json.Marshal(c)
	return fmt.Sprintf("data: %s\n\n", d)
}

// Non-stream response builder
func BuildNonStreamResponse(model, content, thinking string) map[string]interface{} {
	msg := map[string]interface{}{"role": "assistant", "content": content}
	if thinking != "" {
		msg["reasoning_content"] = thinking
	}
	return map[string]interface{}{
		"id": "chatcmpl-" + NewUUID()[:12], "object": "chat.completion",
		"created": time.Now().Unix(), "model": model,
		"choices": []map[string]interface{}{{"index": 0, "message": msg, "finish_reason": "stop"}},
		"usage":   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
}

// BuildToolCallResponse builds a non-stream response with tool_calls
func BuildToolCallResponse(model, content, thinking string, toolCalls []toolcall.ToolCall) map[string]interface{} {
	msg := map[string]interface{}{"role": "assistant"}
	if content != "" {
		msg["content"] = content
	} else {
		msg["content"] = nil
	}
	if thinking != "" {
		msg["reasoning_content"] = thinking
	}

	// Build tool_calls array in OpenAI format
	tc := make([]map[string]interface{}, 0, len(toolCalls))
	for _, call := range toolCalls {
		tc = append(tc, map[string]interface{}{
			"id":   call.ID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	msg["tool_calls"] = tc

	return map[string]interface{}{
		"id": "chatcmpl-" + NewUUID()[:12], "object": "chat.completion",
		"created": time.Now().Unix(), "model": model,
		"choices": []map[string]interface{}{{"index": 0, "message": msg, "finish_reason": "tool_calls"}},
		"usage":   map[string]interface{}{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
}

// Anthropic Stream Translator
type AnthropicTranslator struct {
	Model     string
	RequestID string
	Thinking  strings.Builder
	Answer    strings.Builder
	blockIdx  int
}

func NewAnthropicTranslator(model string) *AnthropicTranslator {
	return &AnthropicTranslator{Model: model, RequestID: "msg_" + NewUUID()[:12]}
}

func (t *AnthropicTranslator) Start() string {
	evt := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": t.RequestID, "type": "message", "role": "assistant", "model": t.Model,
			"content": []interface{}{},
			"usage":   map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		},
	}
	d, _ := json.Marshal(evt)
	return fmt.Sprintf("event: message_start\ndata: %s\n\n", d)
}

func (t *AnthropicTranslator) TranslateEvent(evt *upstream.StreamEvent) string {
	if evt == nil || evt.Type != "delta" || evt.Content == "" {
		return ""
	}
	var sb strings.Builder
	if evt.Phase == "thinking_summary" || evt.Phase == "thinking" {
		if t.Thinking.Len() == 0 {
			bs, _ := json.Marshal(map[string]interface{}{"type": "content_block_start", "index": t.blockIdx, "content_block": map[string]interface{}{"type": "thinking", "thinking": ""}})
			sb.WriteString(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", bs))
		}
		t.Thinking.WriteString(evt.Content)
		d, _ := json.Marshal(map[string]interface{}{"type": "content_block_delta", "index": t.blockIdx, "delta": map[string]interface{}{"type": "thinking_delta", "thinking": evt.Content}})
		sb.WriteString(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", d))
		return sb.String()
	}
	if t.Thinking.Len() > 0 && t.Answer.Len() == 0 {
		stop, _ := json.Marshal(map[string]interface{}{"type": "content_block_stop", "index": t.blockIdx})
		sb.WriteString(fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stop))
		t.blockIdx++
	}
	if t.Answer.Len() == 0 {
		bs, _ := json.Marshal(map[string]interface{}{"type": "content_block_start", "index": t.blockIdx, "content_block": map[string]interface{}{"type": "text", "text": ""}})
		sb.WriteString(fmt.Sprintf("event: content_block_start\ndata: %s\n\n", bs))
	}
	t.Answer.WriteString(evt.Content)
	d, _ := json.Marshal(map[string]interface{}{"type": "content_block_delta", "index": t.blockIdx, "delta": map[string]interface{}{"type": "text_delta", "text": evt.Content}})
	sb.WriteString(fmt.Sprintf("event: content_block_delta\ndata: %s\n\n", d))
	return sb.String()
}

func (t *AnthropicTranslator) Finish() string {
	var sb strings.Builder
	stop, _ := json.Marshal(map[string]interface{}{"type": "content_block_stop", "index": t.blockIdx})
	sb.WriteString(fmt.Sprintf("event: content_block_stop\ndata: %s\n\n", stop))
	md, _ := json.Marshal(map[string]interface{}{"type": "message_delta", "delta": map[string]interface{}{"stop_reason": "end_turn"}, "usage": map[string]interface{}{"output_tokens": 0}})
	sb.WriteString(fmt.Sprintf("event: message_delta\ndata: %s\n\n", md))
	ms, _ := json.Marshal(map[string]interface{}{"type": "message_stop"})
	sb.WriteString(fmt.Sprintf("event: message_stop\ndata: %s\n\n", ms))
	return sb.String()
}

// UUID using crypto/rand
func NewUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
