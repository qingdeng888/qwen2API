package toolcall

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
)

const (
	QNMLOpen           = "<|QNML|tool_calls>"
	QNMLClose          = "</|QNML|tool_calls>"
	QNMLInvokeOpen     = `<|QNML|invoke`
	QNMLInvokeClose    = "</|QNML|invoke>"
	QNMLParameterOpen  = `<|QNML|parameter`
	QNMLParameterClose = "</|QNML|parameter>"
)

// BuildQNMLToolInstructions generates the QNML tool instruction block for the prompt
func BuildQNMLToolInstructions(names []string, toolSchemas []string, heavyProfile bool) string {
	var sb strings.Builder

	// Schema block
	if len(toolSchemas) > 0 {
		sb.WriteString("You have access to these tools:\n\n")
		sb.WriteString(strings.Join(toolSchemas, "\n\n"))
		sb.WriteString("\n\n")
	}

	available := strings.Join(names, ", ")

	// Heavy profile extra rules (Claude Code / Codex)
	extraHeavy := ""
	if heavyProfile {
		extraHeavy = `EXECUTION RULES - CRITICAL:
- When the user gives a task, start immediately by emitting the required QNML tool block if a tool is needed.
- If multiple operations are required, keep using tool calls across turns until the task is complete.
- Do NOT ask for confirmation unless the user explicitly asks you to ask.
- For file/config tasks prefer Read/Edit/Write style tools. Use shell tools only when shell behavior is required.
- If a Read/read_file-style result says the file is unchanged or provides no body, do not repeatedly call the same read request.
- Prefer direct project tools. Use Agent-like delegation tools only when clearly necessary.

`
	}

	// Build examples
	exampleNames := preferredExampleNames(names)
	var examples []string
	if len(exampleNames) > 0 {
		n := exampleNames[0]
		examples = append(examples, fmt.Sprintf(`Example A — Single tool:
<|QNML|tool_calls>
  <|QNML|invoke name="%s">
    <|QNML|parameter name="query"><![CDATA[actual value here]]></|QNML|parameter>
  </|QNML|invoke>
</|QNML|tool_calls>`, n))
	}
	if len(exampleNames) >= 2 {
		examples = append(examples, fmt.Sprintf(`Example B — Two tools in parallel:
<|QNML|tool_calls>
  <|QNML|invoke name="%s">
    <|QNML|parameter name="query"><![CDATA[first actual value]]></|QNML|parameter>
  </|QNML|invoke>
  <|QNML|invoke name="%s">
    <|QNML|parameter name="path"><![CDATA[second actual value]]></|QNML|parameter>
  </|QNML|invoke>
</|QNML|tool_calls>`, exampleNames[0], exampleNames[1]))
	}

	examplesBlock := ""
	if len(examples) > 0 {
		examplesBlock = "\n\nCORRECT EXAMPLES:\n\n" + strings.Join(examples, "\n\n")
	}

	return fmt.Sprintf(`=== QNML TOOL CALL PROTOCOL ===
%sQNML blocks are client-parsed text markers, not native function calls. Use tools only when needed.
Available action names: %s

%sFORMAT:
<|QNML|tool_calls>
  <|QNML|invoke name="TOOL_NAME">
    <|QNML|parameter name="ARG"><![CDATA[value]]></|QNML|parameter>
  </|QNML|invoke>
</|QNML|tool_calls>

CRITICAL RULES:
- ALWAYS wrap parameter values in <![CDATA[...]]>
- NEVER output raw JSON for tool calls. Use ONLY the above QNML format.
- One <|QNML|tool_calls> block may contain multiple <|QNML|invoke> blocks for parallel execution.
- Only use tool names from the Available action names list above.
- If no tool is needed, just reply in plain text.
%s
=== END PROTOCOL ===`, sb.String(), available, extraHeavy, examplesBlock)
}

// RenderQNMLToolCall renders a single tool call in QNML format (for history)
func RenderQNMLToolCall(name string, args map[string]interface{}) string {
	var sb strings.Builder
	sb.WriteString(QNMLOpen + "\n")
	sb.WriteString(fmt.Sprintf(`  <|QNML|invoke name="%s">`+"\n", html.EscapeString(name)))
	for key, value := range args {
		sb.WriteString(fmt.Sprintf(`    <|QNML|parameter name="%s"><![CDATA[%s]]></|QNML|parameter>`+"\n",
			html.EscapeString(key), renderParamValue(value)))
	}
	sb.WriteString("  " + QNMLInvokeClose + "\n")
	sb.WriteString(QNMLClose)
	return sb.String()
}

// ParseQNMLToolCalls parses QNML tool call blocks from model output
func ParseQNMLToolCalls(text string) []ToolCall {
	var calls []ToolCall

	// Find all QNML tool call blocks
	blockRe := regexp.MustCompile(`(?s)<\|QNML\|tool_calls>(.*?)</\|QNML\|tool_calls>`)
	blocks := blockRe.FindAllStringSubmatch(text, -1)

	for _, block := range blocks {
		if len(block) < 2 {
			continue
		}
		// Parse invoke blocks within
		invokeRe := regexp.MustCompile(`(?s)<\|QNML\|invoke\s+name="([^"]+)">(.*?)</\|QNML\|invoke>`)
		invokes := invokeRe.FindAllStringSubmatch(block[1], -1)

		for _, invoke := range invokes {
			if len(invoke) < 3 {
				continue
			}
			name := html.UnescapeString(invoke[1])
			params := parseQNMLParams(invoke[2])
			argsJSON, _ := json.Marshal(params)
			calls = append(calls, ToolCall{
				ID:        "toolu_" + name + "_" + randomID(),
				Name:      name,
				Arguments: string(argsJSON),
			})
		}
	}

	return calls
}

func parseQNMLParams(body string) map[string]interface{} {
	params := make(map[string]interface{})
	// Match parameters with CDATA
	paramRe := regexp.MustCompile(`(?s)<\|QNML\|parameter\s+name="([^"]+)">\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</\|QNML\|parameter>`)
	matches := paramRe.FindAllStringSubmatch(body, -1)
	for _, m := range matches {
		if len(m) >= 3 {
			key := html.UnescapeString(m[1])
			value := m[2]
			// Try to parse as JSON if it looks like JSON
			value = strings.TrimSpace(value)
			if (strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}")) ||
				(strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]")) {
				var jsonVal interface{}
				if json.Unmarshal([]byte(value), &jsonVal) == nil {
					params[key] = jsonVal
					continue
				}
			}
			params[key] = value
		}
	}
	return params
}

func renderParamValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func preferredExampleNames(names []string) []string {
	preferred := []string{"Read", "Bash", "Glob", "Grep", "Write", "Edit"}
	var result []string
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[strings.ToLower(n)] = true
	}
	for _, p := range preferred {
		if nameSet[strings.ToLower(p)] {
			// Find the actual cased name
			for _, n := range names {
				if strings.EqualFold(n, p) {
					result = append(result, n)
					break
				}
			}
		}
		if len(result) >= 2 {
			break
		}
	}
	// Fallback: use first two names
	if len(result) == 0 && len(names) > 0 {
		result = append(result, names[0])
		if len(names) > 1 {
			result = append(result, names[1])
		}
	}
	return result
}

func randomID() string {
	b := make([]byte, 4)
	for i := range b {
		b[i] = "abcdefghijklmnopqrstuvwxyz0123456789"[i%36]
	}
	return string(b)
}
