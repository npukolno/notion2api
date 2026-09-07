package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type toolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type toolDefinition struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolCallResult struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolCallFunction `json:"function"`
}

var toolCallXMLPattern = regexp.MustCompile(`(?s)<tool_calls>\s*(.*?)\s*</tool_calls>`)
var singleCallPattern = regexp.MustCompile(`(?s)<call>\s*<name>(.*?)</name>\s*<arguments>(.*?)</arguments>\s*</call>`)

func parseToolDefinitions(toolsRaw any) []toolDefinition {
	if toolsRaw == nil {
		return nil
	}
	data, err := json.Marshal(toolsRaw)
	if err != nil {
		return nil
	}
	var tools []toolDefinition
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil
	}
	return tools
}

func buildToolSystemPrompt(tools []toolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("You are a coding agent running on the user's own machine. You DO have filesystem and shell access through the tools below. Never claim you cannot access local files, never ask the user to paste terminal output or file contents — always emit a tool call instead.\n\n")
	b.WriteString("You have access to the following tools. When you need to call a tool, you MUST output ONLY the XML block below and nothing else.\n\n")
	b.WriteString("<tools_available>\n")
	for _, t := range tools {
		b.WriteString(fmt.Sprintf("  <tool name=\"%s\">\n", t.Function.Name))
		if t.Function.Description != "" {
			b.WriteString(fmt.Sprintf("    <description>%s</description>\n", t.Function.Description))
		}
		if t.Function.Parameters != nil {
			params, _ := json.Marshal(t.Function.Parameters)
			b.WriteString(fmt.Sprintf("    <parameters>%s</parameters>\n", string(params)))
		}
		b.WriteString("  </tool>\n")
	}
	b.WriteString("</tools_available>\n\n")
	b.WriteString("When calling a tool, use this EXACT format (no markdown, no extra text):\n")
	b.WriteString("<tool_calls>\n")
	b.WriteString("<call>\n")
	b.WriteString("<name>function_name</name>\n")
	b.WriteString("<arguments>{\"param\": \"value\"}</arguments>\n")
	b.WriteString("</call>\n")
	b.WriteString("</tool_calls>\n\n")
	b.WriteString("You may call multiple tools by including multiple <call> blocks.\n")
	b.WriteString("If you do NOT need a tool, respond normally without any XML.\n")
	return b.String()
}

func injectToolsIntoMessages(messagesRaw any, tools []toolDefinition) any {
	if len(tools) == 0 {
		return messagesRaw
	}
	messages := sliceValue(messagesRaw)
	if messages == nil {
		return messagesRaw
	}

	toolPrompt := buildToolSystemPrompt(tools)

	callNames := map[string]string{}
	callArgs := map[string]string{}
	for _, msg := range messages {
		m := mapValue(msg)
		if m == nil {
			continue
		}
		for _, rawCall := range sliceValue(m["tool_calls"]) {
			call := mapValue(rawCall)
			if call == nil {
				continue
			}
			id := strings.TrimSpace(stringValue(call["id"]))
			function := mapValue(call["function"])
			if id == "" || function == nil {
				continue
			}
			callNames[id] = strings.TrimSpace(stringValue(function["name"]))
			callArgs[id] = strings.TrimSpace(stringValue(function["arguments"]))
		}
	}

	hasSystem := false
	result := make([]any, 0, len(messages)+1)
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			result = append(result, msg)
			continue
		}
		role := strings.TrimSpace(stringValue(m["role"]))
		if role == "system" {
			existing := flattenContent(m["content"])
			injected := map[string]any{
				"role":    "system",
				"content": toolPrompt + "\n" + existing,
			}
			result = append(result, injected)
			hasSystem = true
		} else if role == "tool" {
			callID := strings.TrimSpace(stringValue(m["tool_call_id"]))
			name := callNames[callID]
			args := callArgs[callID]
			converted := map[string]any{
				"role":    "user",
				"content": fmt.Sprintf("[Tool Result]\ncall_id: %s\nname: %s\narguments: %s\nresult:\n%s", callID, name, args, flattenContent(m["content"])),
			}
			result = append(result, converted)
		} else {
			result = append(result, msg)
		}
	}
	if !hasSystem {
		systemMsg := map[string]any{
			"role":    "system",
			"content": toolPrompt,
		}
		result = append([]any{systemMsg}, result...)
	}
	return result
}

func parseToolCallsFromResponse(text string) ([]toolCallResult, string) {
	match := toolCallXMLPattern.FindStringSubmatch(text)
	if match == nil {
		return nil, text
	}

	callsXML := match[1]
	calls := singleCallPattern.FindAllStringSubmatch(callsXML, -1)
	if len(calls) == 0 {
		return nil, text
	}

	results := make([]toolCallResult, 0, len(calls))
	for i, call := range calls {
		name := strings.TrimSpace(call[1])
		args := strings.TrimSpace(call[2])
		if !json.Valid([]byte(args)) {
			args = "{}"
		}
		results = append(results, toolCallResult{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: toolCallFunction{
				Name:      name,
				Arguments: args,
			},
		})
	}

	remaining := strings.TrimSpace(toolCallXMLPattern.ReplaceAllString(text, ""))
	return results, remaining
}

func requestedToolChoiceName(raw any) string {
	choice := mapValue(raw)
	if choice == nil {
		return ""
	}
	function := mapValue(choice["function"])
	if function == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(function["name"]))
}

func toolChoiceMode(raw any) string {
	mode := strings.ToLower(strings.TrimSpace(stringValue(raw)))
	if mode != "" {
		return mode
	}
	if requestedToolChoiceName(raw) != "" {
		return "required"
	}
	return "auto"
}

func normalizedToolName(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"), " ", "_"))
}

func messagesContainToolResult(messagesRaw any) bool {
	for _, raw := range sliceValue(messagesRaw) {
		m := mapValue(raw)
		if m != nil && strings.TrimSpace(stringValue(m["role"])) == "tool" {
			return true
		}
	}
	return false
}

func latestUserTextFromMessages(messagesRaw any) string {
	messages := sliceValue(messagesRaw)
	for i := len(messages) - 1; i >= 0; i-- {
		m := mapValue(messages[i])
		if m == nil || strings.TrimSpace(stringValue(m["role"])) != "user" {
			continue
		}
		if text := strings.TrimSpace(flattenContent(m["content"])); text != "" {
			return text
		}
	}
	return ""
}

func quotedFragments(text string) []string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile("`([^`]+)`"),
		regexp.MustCompile(`"([^"]+)"`),
		regexp.MustCompile(`'([^']+)'`),
	}
	out := []string{}
	for _, pattern := range patterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
				out = append(out, strings.TrimSpace(match[1]))
			}
		}
	}
	return out
}

func extractLikelyLocation(prompt string) string {
	clean := strings.TrimSpace(prompt)
	if clean == "" {
		return ""
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:in|for|at)\s+([A-Za-zÀ-ỹ][A-Za-zÀ-ỹ\s._-]{1,60})(?:[?.!,]|$)`),
		regexp.MustCompile(`(?i)weather\s+([A-Za-zÀ-ỹ][A-Za-zÀ-ỹ\s._-]{1,60})(?:[?.!,]|$)`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(clean)
		if len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

func extractLikelyPath(prompt string) string {
	for _, fragment := range quotedFragments(prompt) {
		if looksLikePath(fragment) {
			return fragment
		}
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(~/(?:[^\s'"` + "`" + `]+/?)+)`),
		regexp.MustCompile(`(?i)(/(?:[^\s'"` + "`" + `]+/?)+)`),
		regexp.MustCompile(`(?i)(\.\.?/(?:[^\s'"` + "`" + `]+/?)+)`),
		regexp.MustCompile(`(?i)\b([\w.-]+/[\w./-]+)`),
		regexp.MustCompile(`(?i)\b([\w.-]+\.(?:go|js|ts|tsx|jsx|py|json|md|txt|yaml|yml|toml|rs|java|cpp|c|h|html|css|sh))\b`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(prompt)
		if len(match) == 2 {
			return strings.TrimRight(strings.TrimSpace(match[1]), ".,;:")
		}
	}
	return ""
}

func looksLikePath(value string) bool {
	clean := strings.TrimSpace(value)
	return strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "./") || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/") || regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`).MatchString(clean)
}

func extractSearchPattern(prompt string) string {
	fragments := quotedFragments(prompt)
	if len(fragments) > 0 {
		for _, fragment := range fragments {
			if !looksLikePath(fragment) {
				return fragment
			}
		}
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:search for|grep for|find pattern|pattern)\s+(.+?)(?:\s+in\s+|$)`),
		regexp.MustCompile(`(?i)(?:grep|rg)\s+([^\s]+)`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(prompt)
		if len(match) == 2 {
			return strings.Trim(strings.TrimSpace(match[1]), "'\".,;:")
		}
	}
	return ""
}

func extractCommand(prompt string) string {
	fragments := quotedFragments(prompt)
	if len(fragments) > 0 {
		return fragments[0]
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:run|execute)\s+(?:command\s+)?(.+)$`),
		regexp.MustCompile(`(?i)(?:shell|terminal)\s*:?\s*(.+)$`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(prompt)
		if len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

func extractReplacementPair(prompt string) (string, string) {
	fragments := quotedFragments(prompt)
	if len(fragments) >= 2 {
		return fragments[0], fragments[1]
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?is)replace\s+(.+?)\s+with\s+(.+?)(?:\s+in\s+|$)`),
		regexp.MustCompile(`(?is)change\s+(.+?)\s+to\s+(.+?)(?:\s+in\s+|$)`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(prompt)
		if len(match) == 3 {
			return strings.Trim(strings.TrimSpace(match[1]), "'\"`"), strings.Trim(strings.TrimSpace(match[2]), "'\"`")
		}
	}
	return "", ""
}

func extractContent(prompt string) string {
	fragments := quotedFragments(prompt)
	if len(fragments) > 0 {
		return fragments[len(fragments)-1]
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?is)(?:with content|content:)\s*(.+)$`),
		regexp.MustCompile(`(?is)(?:to say|write)\s+(.+)$`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(prompt)
		if len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

func requiredParamNames(tool toolDefinition) map[string]bool {
	out := map[string]bool{}
	for _, value := range sliceValue(tool.Function.Parameters["required"]) {
		name := strings.TrimSpace(stringValue(value))
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func chooseTool(prompt string, tools []toolDefinition, toolChoice any) (toolDefinition, bool) {
	if len(tools) == 0 {
		return toolDefinition{}, false
	}
	chosenName := requestedToolChoiceName(toolChoice)
	if chosenName != "" {
		for _, tool := range tools {
			if normalizedToolName(tool.Function.Name) == normalizedToolName(chosenName) {
				return tool, true
			}
		}
		return toolDefinition{}, false
	}
	lowerPrompt := strings.ToLower(prompt)
	bestScore := -1
	best := tools[0]
	// Tie-break by path shape: a file-like path favors read/edit tools,
	// a directory-like path favors listing/search tools.
	shapedPath := extractLikelyPath(prompt)
	pathIsFile := shapedPath != "" && regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`).MatchString(shapedPath)
	for _, tool := range tools {
		name := normalizedToolName(tool.Function.Name)
		desc := strings.ToLower(tool.Function.Description)
		score := 0
		if strings.Contains(lowerPrompt, strings.ToLower(tool.Function.Name)) || strings.Contains(lowerPrompt, name) {
			score += 100
		}
		for _, keyword := range intentKeywordsForTool(name, desc) {
			if strings.Contains(lowerPrompt, keyword) {
				score += 10
			}
		}
		famText := name + " " + desc
		isReader := strings.Contains(famText, "read") || strings.Contains(famText, "file") || strings.Contains(famText, "cat") || strings.Contains(famText, "edit") || strings.Contains(famText, "replace") || strings.Contains(famText, "patch")
		isFinder := strings.Contains(famText, "list") || strings.Contains(famText, "dir") || strings.Contains(famText, "ls") || strings.Contains(famText, "grep") || strings.Contains(famText, "search") || strings.Contains(famText, "find") || strings.Contains(famText, "glob") || strings.Contains(famText, "pattern")
		if pathIsFile && isReader {
			score += 5
		}
		if !pathIsFile && shapedPath != "" && isFinder {
			score += 5
		}
		if score > bestScore {
			bestScore = score
			best = tool
		}
	}
	mode := toolChoiceMode(toolChoice)
	if bestScore <= 0 && mode != "required" {
		return toolDefinition{}, false
	}
	return best, true
}

func intentKeywordsForTool(name string, description string) []string {
	text := name + " " + description
	keywords := []string{}
	add := func(values ...string) { keywords = append(keywords, values...) }
	// Generic file-system words (RU + EN): attached to every file-related
	// family so Russian prompts ("посмотри файлы в ...") match even when the
	// tool description is English. Score ties are acceptable — the agent loop
	// course-corrects on the next turn.
	fileWordsRU := []string{"файл", "файлы", "файлов", "папка", "папку", "папке", "директория", "директорию", "путь", "пути", "проект", "код", "посмотри", "посмотреть", "покажи", "показать"}
	if strings.Contains(text, "read") || strings.Contains(text, "file") || strings.Contains(text, "cat") {
		add("read", "open", "view", "inspect", "show file", "xem", "đọc")
		add("прочитай", "прочти", "прочитать", "открой", "открыть", "покажи файл", "посмотри файл", "содержимое файла", "что внутри", "покажи код")
		add(fileWordsRU...)
	}
	if strings.Contains(text, "edit") || strings.Contains(text, "replace") || strings.Contains(text, "patch") || strings.Contains(text, "modify") {
		add("edit", "replace", "change", "modify", "patch", "update", "sửa")
		add("исправь", "исправить", "измени", "изменить", "поменяй", "замени", "заменить", "отредактируй", "добавь", "добавить", "удали", "удалить", "поправь")
		add(fileWordsRU...)
	}
	if strings.Contains(text, "create") || strings.Contains(text, "write") {
		add("create", "write", "new file", "save")
		add("создай", "создать", "напиши", "написать", "запиши", "записать", "сохрани", "сохранить", "создай файл")
		add(fileWordsRU...)
	}
	if strings.Contains(text, "list") || strings.Contains(text, "dir") || strings.Contains(text, "ls") {
		add("list", "ls", "directory", "folder")
		add("посмотри", "посмотреть", "покажи", "показать", "список", "что за файлы", "что внутри папки", "содержимое папки", "перечисли")
		add(fileWordsRU...)
	}
	if strings.Contains(text, "grep") || strings.Contains(text, "search") || strings.Contains(text, "find") || strings.Contains(text, "glob") || strings.Contains(text, "pattern") {
		add("grep", "search", "find", "rg", "pattern")
		add("найди", "найти", "поиск", "поищи", "где используется", "где находится", "содержит")
		add(fileWordsRU...)
	}
	if strings.Contains(text, "execute") || strings.Contains(text, "command") || strings.Contains(text, "shell") || strings.Contains(text, "run") || strings.Contains(text, "bash") || strings.Contains(text, "terminal") {
		add("execute", "run", "command", "shell", "terminal")
		add("запусти", "запустить", "выполни", "выполнить", "команда", "команду", "терминал", "собери", "собрать", "установи", "проверь командой")
	}
	if strings.Contains(text, "weather") || strings.Contains(text, "location") {
		add("weather", "city", "location")
	}
	return keywords
}

func inferArgumentValue(prompt string, tool toolDefinition, name string, schema any) (any, bool) {
	lowerName := strings.ToLower(name)
	param := mapValue(schema)
	paramType := strings.ToLower(strings.TrimSpace(stringValue(param["type"])))
	toolName := normalizedToolName(tool.Function.Name)
	if paramType == "" {
		paramType = "string"
	}
	if paramType == "string" {
		switch {
		case strings.Contains(lowerName, "path") || strings.Contains(lowerName, "file") || strings.Contains(lowerName, "directory") || strings.Contains(lowerName, "dir"):
			if value := extractLikelyPath(prompt); value != "" {
				return value, true
			}
		case strings.Contains(lowerName, "pattern") || strings.Contains(lowerName, "query") || strings.Contains(lowerName, "regex"):
			if value := extractSearchPattern(prompt); value != "" {
				return value, true
			}
			// "посмотри файлы в <dir>" — no explicit pattern, but a path is
			// present: use it as a glob base so directory listings work.
			if value := extractLikelyPath(prompt); value != "" {
				if !strings.ContainsAny(value, "*?[") {
					value += "/**"
				}
				return value, true
			}
		case strings.Contains(lowerName, "command") || strings.Contains(lowerName, "cmd"):
			if value := extractCommand(prompt); value != "" {
				return value, true
			}
		case strings.Contains(lowerName, "old") || strings.Contains(lowerName, "find"):
			oldValue, _ := extractReplacementPair(prompt)
			if oldValue != "" {
				return oldValue, true
			}
		case strings.Contains(lowerName, "new") || strings.Contains(lowerName, "replace"):
			_, newValue := extractReplacementPair(prompt)
			if newValue != "" {
				return newValue, true
			}
		case strings.Contains(lowerName, "content") || strings.Contains(lowerName, "text"):
			if value := extractContent(prompt); value != "" {
				return value, true
			}
		case strings.Contains(lowerName, "city") || strings.Contains(lowerName, "location"):
			if value := extractLikelyLocation(prompt); value != "" {
				return value, true
			}
		}
		if strings.Contains(toolName, "read") && (lowerName == "path" || lowerName == "file_path") {
			if value := extractLikelyPath(prompt); value != "" {
				return value, true
			}
		}
		if strings.Contains(toolName, "execute") || strings.Contains(toolName, "command") {
			if value := extractCommand(prompt); value != "" {
				return value, true
			}
		}
		if enums := sliceValue(param["enum"]); len(enums) > 0 {
			lowerPrompt := strings.ToLower(prompt)
			for _, enum := range enums {
				value := stringValue(enum)
				if value != "" && strings.Contains(lowerPrompt, strings.ToLower(value)) {
					return value, true
				}
			}
		}
	}
	if paramType == "boolean" {
		lowerPrompt := strings.ToLower(prompt)
		if strings.Contains(lowerPrompt, lowerName) {
			return !strings.Contains(lowerPrompt, "not "+lowerName) && !strings.Contains(lowerPrompt, "no "+lowerName), true
		}
	}
	return nil, false
}

func synthesizeToolCallFromMessages(messagesRaw any, tools []toolDefinition, toolChoice any) ([]toolCallResult, bool) {
	if messagesContainToolResult(messagesRaw) {
		return nil, false
	}
	return synthesizeToolCall(latestUserTextFromMessages(messagesRaw), tools, toolChoice)
}

func synthesizeToolCall(prompt string, tools []toolDefinition, toolChoice any) ([]toolCallResult, bool) {
	if len(tools) == 0 {
		return nil, false
	}
	if toolChoiceMode(toolChoice) == "none" {
		return nil, false
	}
	chosen, ok := chooseTool(prompt, tools, toolChoice)
	if !ok {
		return nil, false
	}
	args := map[string]any{}
	params := mapValue(chosen.Function.Parameters["properties"])
	for name, schema := range params {
		if value, ok := inferArgumentValue(prompt, chosen, name, schema); ok {
			args[name] = value
		}
	}
	required := requiredParamNames(chosen)
	for name := range required {
		if _, ok := args[name]; !ok {
			return nil, false
		}
	}
	argsJSON, _ := json.Marshal(args)
	return []toolCallResult{{
		ID:   "call_0",
		Type: "function",
		Function: toolCallFunction{
			Name:      chosen.Function.Name,
			Arguments: string(argsJSON),
		},
	}}, true
}

func buildSyntheticToolCallCompletion(prompt string, toolCalls []toolCallResult, modelID string) map[string]any {
	tcJSON := make([]map[string]any, 0, len(toolCalls))
	for _, tc := range toolCalls {
		tcJSON = append(tcJSON, map[string]any{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]any{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		})
	}
	message := map[string]any{
		"role":       "assistant",
		"content":    nil,
		"tool_calls": tcJSON,
	}
	return map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "tool_calls",
		}},
		"usage":              buildUsage(prompt, "", ""),
		"system_fingerprint": "notion2api-local-go",
	}
}

func buildChatCompletionWithToolCalls(result InferenceResult, modelID string, includeTrace bool, hasTools bool) map[string]any {
	assistantText := sanitizeAssistantVisibleText(result.Text)
	reasoningText := sanitizeAssistantVisibleText(result.Reasoning)

	message := map[string]any{
		"role": "assistant",
	}

	finishReason := "stop"

	if hasTools {
		toolCalls, remaining := parseToolCallsFromResponse(assistantText)
		if len(toolCalls) > 0 {
			message["content"] = nil
			if remaining != "" {
				message["content"] = remaining
			}
			tcJSON := make([]map[string]any, 0, len(toolCalls))
			for _, tc := range toolCalls {
				tcJSON = append(tcJSON, map[string]any{
					"id":   tc.ID,
					"type": tc.Type,
					"function": map[string]any{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				})
			}
			message["tool_calls"] = tcJSON
			finishReason = "tool_calls"
		} else {
			message["content"] = assistantText
		}
	} else {
		message["content"] = assistantText
	}

	attachChatReasoningFields(message, reasoningText)

	payload := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage":              buildUsage(result.Prompt, assistantText, reasoningText),
		"system_fingerprint": "notion2api-local-go",
	}
	if includeTrace {
		payload["notion_trace"] = buildTrace(result)
	}
	return payload
}

// requestedModelForLog / toolChoiceForLog: one-line request logging helpers.
func requestedModelForLog(raw any) string {
	if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if s := strings.TrimSpace(stringValue(raw)); s != "" {
		return s
	}
	return "?"
}

func toolChoiceForLog(raw any) string {
	if raw == nil {
		return "auto"
	}
	if s, ok := raw.(string); ok {
		if strings.TrimSpace(s) == "" {
			return "auto"
		}
		return strings.TrimSpace(s)
	}
	if name := requestedToolChoiceName(raw); name != "" {
		return "function:" + name
	}
	return toolChoiceMode(raw)
}
