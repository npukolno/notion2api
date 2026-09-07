package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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
	if strings.Contains(text, "task") || strings.Contains(text, "agent") || strings.Contains(text, "delegate") || strings.Contains(text, "subagent") || strings.Contains(text, "beru") || strings.Contains(text, "igris") || strings.Contains(text, "tusk") || strings.Contains(text, "tank") || strings.Contains(text, "bellion") {
		add("task", "agent", "subagent", "delegate", "beru", "background")
		add("задача", "задачу", "вызови", "вызвать", "делегируй", "делегировать", "подагент", "агент", "агента", "поручи", "поручить", "пусть beru", "beru")
		add(fileWordsRU...)
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
		case strings.Contains(lowerName, "subagent") || lowerName == "agent" || lowerName == "agent_type" || lowerName == "shadow":
			if value := extractSubagentType(prompt); value != "" {
				return value, true
			}
		case lowerName == "prompt" || lowerName == "task" || lowerName == "query_text":
			if value := strings.TrimSpace(prompt); value != "" {
				return value, true
			}
		case lowerName == "description" || lowerName == "title" || lowerName == "summary":
			if value := firstWords(prompt, 5); value != "" {
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
	// Explicit tool_choice name → that tool only (legacy behavior).
	if chosenName := requestedToolChoiceName(toolChoice); chosenName != "" {
		for _, tool := range tools {
			if normalizedToolName(tool.Function.Name) == normalizedToolName(chosenName) {
				return buildToolCallWithArgs(prompt, tool, "call_0")
			}
		}
		return nil, false
	}
	// Otherwise rank ALL tools by score and take the first one whose required
	// args can actually be inferred. A high score with uninferred args (e.g.
	// an orchestrator tool needing structured params) must not block a
	// slightly lower-scoring but fully inferable tool (e.g. task/read/glob).
	ranked := rankToolsByScore(prompt, tools, toolChoiceMode(toolChoice))
	for _, rt := range ranked {
		if calls, ok := buildToolCallWithArgs(prompt, rt.def, "call_0"); ok {
			return calls, true
		}
	}
	return nil, false
}

// rankToolsByScore orders tools by prompt-match score (highest first).
// Tools scoring <= 0 are dropped unless mode is "required".
func rankToolsByScore(prompt string, tools []toolDefinition, mode string) []scoredToolDef {
	lowerPrompt := strings.ToLower(prompt)
	shapedPath := extractLikelyPath(prompt)
	pathIsFile := shapedPath != "" && regexp.MustCompile(`\.[A-Za-z0-9]{1,8}$`).MatchString(shapedPath)
	out := []scoredToolDef{}
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
		// Shadow-agent names (beru/igris/...) belong to the arise_* tools:
		// boost them so delegation prompts don't land on the generic task tool
		// (whose subagent types may not include arise shadows).
		if isAriseToolDef(name, desc) && mentionsShadowName(lowerPrompt) {
			score += 60
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
		if score <= 0 && mode != "required" {
			continue
		}
		out = append(out, scoredToolDef{def: tool, score: score})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].score > out[b].score })
	return out
}

type scoredToolDef struct {
	def   toolDefinition
	score int
}

// buildToolCallWithArgs infers args for one tool; false if any required arg
// cannot be inferred from the prompt.
func buildToolCallWithArgs(prompt string, chosen toolDefinition, callID string) ([]toolCallResult, bool) {
	args := map[string]any{}
	params := mapValue(chosen.Function.Parameters["properties"])
	for name, schema := range params {
		if value, ok := inferArgumentValue(prompt, chosen, name, schema); ok {
			args[name] = value
		}
	}
	for name := range requiredParamNames(chosen) {
		if _, ok := args[name]; !ok {
			return nil, false
		}
	}
	argsJSON, _ := json.Marshal(args)
	return []toolCallResult{{
		ID:   callID,
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

// firstWords: first N words of a text (for short description params).
func firstWords(text string, n int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	if len(words) > n {
		words = words[:n]
	}
	return strings.Join(words, " ")
}

// extractSubagentType: known opencode/arise subagent types mentioned in text.
func extractSubagentType(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, t := range []string{"beru", "igris", "bellion", "tusk", "tank", "build", "plan", "explore", "general"} {
		if strings.Contains(lower, t) {
			return t
		}
	}
	return "general"
}

// priorAssistantToolCalls: all tool calls the assistant already made in this
// conversation (to avoid repeating the same call and to cap chain depth).
func priorAssistantToolCalls(messagesRaw any) []toolCallFunction {
	out := []toolCallFunction{}
	for _, raw := range sliceValue(messagesRaw) {
		m := mapValue(raw)
		if m == nil {
			continue
		}
		for _, rawCall := range sliceValue(m["tool_calls"]) {
			call := mapValue(rawCall)
			if call == nil {
				continue
			}
			fn := mapValue(call["function"])
			if fn == nil {
				continue
			}
			out = append(out, toolCallFunction{
				Name:      strings.TrimSpace(stringValue(fn["name"])),
				Arguments: strings.TrimSpace(stringValue(fn["arguments"])),
			})
		}
	}
	return out
}

func sameToolCall(a toolCallFunction, name, args string) bool {
	return normalizedToolName(a.Name) == normalizedToolName(name) && strings.TrimSpace(a.Arguments) == strings.TrimSpace(args)
}

// lastToolResultContent: content of the most recent role=tool message.
func lastToolResultContent(messagesRaw any) (string, bool) {
	msgs := sliceValue(messagesRaw)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := mapValue(msgs[i])
		if m == nil {
			continue
		}
		if strings.TrimSpace(stringValue(m["role"])) != "tool" {
			continue
		}
		return flattenContent(m["content"]), true
	}
	return "", false
}

var dirHintPattern = regexp.MustCompile(`(?i)is a directory|not a file|ENOTDIR|является (папкой|директорией)|это (папка|директория)`)
var deadEndPattern = regexp.MustCompile(`(?i)no such file|not found|ENOENT|не найден|нет такого|permission denied|EACCES|отказано в доступе|invalid arguments|unknown tool|not allowed`)

// synthesizeFollowupCall: after tool results came back, decide the next step.
// Returns (calls, true) for exactly one follow-up (directory hint), or
// (nil, false) meaning the loop should terminate with a final text answer.
func synthesizeFollowupCall(messagesRaw any, tools []toolDefinition) ([]toolCallResult, bool) {
	result, ok := lastToolResultContent(messagesRaw)
	if !ok || len(tools) == 0 {
		return nil, false
	}
	prior := priorAssistantToolCalls(messagesRaw)
	if len(prior) >= 2 {
		return nil, false // depth cap: max 2 synthesized calls per conversation
	}
	if !dirHintPattern.MatchString(result) {
		return nil, false
	}
	goal := latestUserTextFromMessages(messagesRaw)
	combined := strings.TrimSpace(goal) + "\nPrevious tool result:\n" + strings.TrimSpace(result)
	// Prefer listing/search tools for the follow-up.
		bestScore := -1
	type scoredTool struct {
		def   toolDefinition
		score int
	}
	ranked := []scoredTool{}
	lowerCombined := strings.ToLower(combined)
	for i := range tools {
		t := tools[i]
		famText := normalizedToolName(t.Function.Name) + " " + strings.ToLower(t.Function.Description)
		isFinder := strings.Contains(famText, "list") || strings.Contains(famText, "dir") || strings.Contains(famText, "ls") || strings.Contains(famText, "grep") || strings.Contains(famText, "search") || strings.Contains(famText, "find") || strings.Contains(famText, "glob") || strings.Contains(famText, "pattern")
		if !isFinder {
			continue
		}
		score := 0
		if strings.Contains(lowerCombined, normalizedToolName(t.Function.Name)) {
			score += 100 // the error text itself names the right tool
		}
		for _, kw := range intentKeywordsForTool(normalizedToolName(t.Function.Name), strings.ToLower(t.Function.Description)) {
			if strings.Contains(lowerCombined, kw) {
				score += 10
			}
		}
		if score > bestScore {
			bestScore = score
		}
		ranked = append(ranked, scoredTool{def: t, score: score})
	}
	if len(ranked) == 0 || bestScore <= 0 {
		return nil, false
	}
	sort.Slice(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })
	// Build args directly (no auto-gate here — the follow-up was explicitly
	// requested by the error signal, not by prompt matching). Try candidates
	// in score order, skipping exact repeats of prior calls.
	for ci := range ranked {
		best := ranked[ci].def
		args := map[string]any{}
		params := mapValue(best.Function.Parameters["properties"])
		for name, schema := range params {
			if value, ok := inferArgumentValue(combined, best, name, schema); ok {
				args[name] = value
			}
		}
		complete := true
		for name := range requiredParamNames(best) {
			if _, ok := args[name]; !ok {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		argsJSON, _ := json.Marshal(args)
		dup := false
		for _, p := range prior {
			if sameToolCall(p, best.Function.Name, string(argsJSON)) {
				dup = true
				break
			}
		}
		if dup {
			continue // already tried exactly this — try next candidate
		}
		return []toolCallResult{{
			ID:   fmt.Sprintf("call_%d", len(prior)),
			Type: "function",
			Function: toolCallFunction{
				Name:      best.Function.Name,
				Arguments: string(argsJSON),
			},
		}}, true
	}
	return nil, false
}

// finalTextAfterTools: terminal answer once tool results are in and no
// follow-up applies — surface the last result as the assistant message so the
// agent loop ends with data instead of hanging.
func finalTextAfterTools(messagesRaw any) (string, bool) {
	result, ok := lastToolResultContent(messagesRaw)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(result)
	if text == "" {
		return "", false
	}
	if len(text) > 4000 {
		text = text[:4000] + "\n…(truncated)"
	}
	return text, true
}

func buildFinalTextCompletion(latestPrompt string, text string, modelID string) map[string]any {
	_ = latestPrompt
	return map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
}

// isAriseToolDef: arise_* shadow-agent tools (beru/igris/... executors).
func isAriseToolDef(name, desc string) bool {
	if strings.HasPrefix(name, "arise_") {
		return true
	}
	combined := name + " " + desc
	return strings.Contains(combined, "shadow agent") || strings.Contains(combined, "shadow-agent")
}

// mentionsShadowName: prompt names a specific shadow (beru/igris/...).
func mentionsShadowName(lowerPrompt string) bool {
	for _, s := range []string{"beru", "igris", "bellion", "tusk", "tank", "shadow-sovereign", "esil"} {
		if strings.Contains(lowerPrompt, s) {
			return true
		}
	}
	return false
}
