package upstream

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type StreamEvent struct {
	Type    string // "delta", "error"
	Phase   string // "answer", "thinking_summary"
	Content string
	Status  string
	Extra   map[string]interface{}
}

func ConsumeSSE(reader io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)

	lineCount := 0
	var buf strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		lineCount++
		if line == "" {
			if buf.Len() > 0 {
				for _, evt := range parseChunk(buf.String()) {
					ch <- evt
				}
				buf.Reset()
			}
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if buf.Len() > 0 {
		for _, evt := range parseChunk(buf.String()) {
			ch <- evt
		}
	}
	if err := scanner.Err(); err != nil {
		ch <- StreamEvent{Type: "error", Content: "SSE read error: " + err.Error()}
	}
	if lineCount == 0 {
		ch <- StreamEvent{Type: "error", Content: "SSE stream returned empty response"}
	}
}

func parseChunk(chunk string) []StreamEvent {
	var events []StreamEvent
	for _, line := range strings.Split(chunk, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" || data == "[DONE]" {
			continue
		}
		var obj map[string]interface{}
		if json.Unmarshal([]byte(data), &obj) != nil {
			continue
		}
		if errMsg := extractError(obj); errMsg != "" {
			events = append(events, StreamEvent{Type: "error", Content: errMsg})
			continue
		}
		choices, _ := obj["choices"].([]interface{})
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]interface{})
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]interface{})
		if delta == nil {
			continue
		}
		phase := strField(delta, "phase", "answer")
		content := strField(delta, "content", "")
		reasoning := extractReasoning(delta)
		if reasoning != "" {
			content = reasoning
			if phase == "answer" {
				phase = "thinking_summary"
			}
		}
		extra, _ := delta["extra"].(map[string]interface{})
		if extra == nil {
			extra = map[string]interface{}{}
		}
		events = append(events, StreamEvent{Type: "delta", Phase: phase, Content: content, Status: strField(delta, "status", ""), Extra: extra})
	}
	return events
}

func extractReasoning(delta map[string]interface{}) string {
	extra, _ := delta["extra"].(map[string]interface{})
	keys := []string{"reasoning_content", "reasoning", "reasoning_text", "thinking", "thoughts"}
	for _, k := range keys {
		if v := strField(delta, k, ""); v != "" {
			return v
		}
	}
	if extra != nil {
		for _, k := range keys {
			if v := strField(extra, k, ""); v != "" {
				return v
			}
		}
	}
	return ""
}

func extractError(obj map[string]interface{}) string {
	if s, ok := obj["success"].(bool); ok && !s {
		d, _ := obj["data"].(map[string]interface{})
		code := strField(d, "code", strField(obj, "code", "upstream_error"))
		msg := strField(d, "details", strField(d, "message", strField(obj, "message", "")))
		return "Qwen error code=" + code + " " + msg
	}
	if e, ok := obj["error"]; ok {
		switch v := e.(type) {
		case map[string]interface{}:
			return "Qwen error " + strField(v, "code", "") + " " + strField(v, "message", "")
		case string:
			if v != "" {
				return "Qwen error: " + v
			}
		}
	}
	return ""
}

func strField(m map[string]interface{}, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}
