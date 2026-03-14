package main

// =============================================================================
// prompt.go — 系统提示构建 + 消息转换
//
// 系统提示的结构:
//   1. 角色定义
//   2. 可用工具列表（根据实际启用的工具动态生成）
//   3. 使用指南（根据工具组合变化）
//   4. 当前工作目录和日期
// =============================================================================

import (
	"fmt"
	"strings"
	"time"
)

// BuildSystemPrompt 构建系统提示
func BuildSystemPrompt(tools []Tool, cwd string) string {
	var sb strings.Builder

	sb.WriteString(`You are an expert coding assistant created by [SataQiu](https://github.com/SataQiu). You help users by reading files, executing commands, editing code, and writing new files.

`)

	sb.WriteString("Available tools:\n")
	for _, t := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
	}

	sb.WriteString("\nGuidelines:\n")

	toolSet := make(map[string]bool)
	for _, t := range tools {
		toolSet[t.Name] = true
	}

	if toolSet["read"] && toolSet["edit"] {
		sb.WriteString("- Use read to examine files before editing. You must use this tool instead of cat or sed.\n")
	}
	if toolSet["edit"] {
		sb.WriteString("- Use edit for precise changes (old_text must match exactly).\n")
	}
	if toolSet["write"] {
		sb.WriteString("- Use write only for new files or complete rewrites.\n")
	}
	if toolSet["bash"] {
		sb.WriteString("- Use bash for running builds, tests, git commands, and file exploration.\n")
	}

	sb.WriteString("- Be concise in your responses.\n")
	sb.WriteString("- Show file paths clearly when working with files.\n")

	sb.WriteString(fmt.Sprintf("\nCurrent date: %s\n", time.Now().Format("2006-01-02")))
	sb.WriteString(fmt.Sprintf("Current working directory: %s\n", cwd))

	return sb.String()
}

// PrepareMessages 将会话消息转换为 LLM API 调用格式
// 在消息列表前插入系统提示作为第一条消息
func PrepareMessages(systemPrompt string, messages []ChatMessage) []ChatMessage {
	result := make([]ChatMessage, 0, len(messages)+1)

	result = append(result, ChatMessage{
		Role:    "system",
		Content: systemPrompt,
	})

	result = append(result, messages...)

	return result
}
