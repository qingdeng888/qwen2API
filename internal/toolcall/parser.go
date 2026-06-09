package toolcall

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ToolCall represents a parsed tool call from model output
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Parse attempts to extract tool calls from model output (multi-format: QNML, JSON, XML)
func Parse(text string) []ToolCall {
	if calls := parseQNML(text); len(calls) > 0 {
		return calls
	}
	if calls := parseJSON(text); len(calls) > 0 {
		return calls
	}
	if calls := parseXML(text); len(calls) > 0 {
		return calls
	}
	return nil
}

func parseQNML(text string) []ToolCall {
	re := regexp.MustCompile(`(?s)<\|(?:qnml|QNML)\|(?:tool_calls|invoke)>\s*(\{.+?\})`)
	matches := re.FindAllStringSubmatch(text, -1)
	var calls []ToolCall
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if tc := parseToolJSON(m[1]); tc != nil {
			calls = append(calls, *tc)
		}
	}
	re2 := regexp.MustCompile(`(?s)##TOOL_CALL##\s*(\{.+?\})`)
	for _, m := range re2.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			if tc := parseToolJSON(m[1]); tc != nil {
				calls = append(calls, *tc)
			}
		}
	}
	return calls
}

func parseJSON(text string) []ToolCall {
	re := regexp.MustCompile(`(?s)\{[^{}]*"name"\s*:\s*"[^"]+"\s*,\s*"(?:arguments|input)"\s*:\s*\{[^{}]*\}[^{}]*\}`)
	var calls []ToolCall
	for _, m := range re.FindAllString(text, -1) {
		if tc := parseToolJSON(m); tc != nil {
			calls = append(calls, *tc)
		}
	}
	return calls
}

func parseXML(text string) []ToolCall {
	re := regexp.MustCompile(`(?s)<tool_calls>\s*<invoke\s+name="([^"]+)">(.*?)</invoke>`)
	var calls []ToolCall
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if len(m) < 3 {
			continue
		}
		params := parseXMLParams(m[2])
		args, _ := json.Marshal(params)
		calls = append(calls, ToolCall{ID: "call_" + m[1], Name: m[1], Arguments: string(args)})
	}
	return calls
}

func parseXMLParams(body string) map[string]string {
	re := regexp.MustCompile(`<parameter\s+name="([^"]+)">(.*?)</parameter>`)
	params := make(map[string]string)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if len(m) >= 3 {
			params[m[1]] = m[2]
		}
	}
	return params
}

func parseToolJSON(s string) *ToolCall {
	var obj map[string]interface{}
	if json.Unmarshal([]byte(s), &obj) != nil {
		return nil
	}
	name, _ := obj["name"].(string)
	if name == "" {
		return nil
	}
	id, _ := obj["id"].(string)
	if id == "" {
		id = "call_" + name
	}
	var args string
	if a, ok := obj["arguments"]; ok {
		d, _ := json.Marshal(a)
		args = string(d)
	} else if a, ok := obj["input"]; ok {
		d, _ := json.Marshal(a)
		args = string(d)
	} else {
		args = "{}"
	}
	return &ToolCall{ID: id, Name: name, Arguments: args}
}

// NormalizeName fuzzy-matches tool names
func NormalizeName(name string, registry map[string]string) string {
	if _, ok := registry[name]; ok {
		return name
	}
	if orig, ok := registry[strings.ToLower(name)]; ok {
		return orig
	}
	return name
}
