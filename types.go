package main

// =============================================================================
// types.go — 所有共享类型定义
// =============================================================================

import "encoding/json"

// ---------------------------------------------------------------------------
// OpenAI 兼容的消息格式 (LiteLLM proxy 使用此格式)
// ---------------------------------------------------------------------------

// ChatMessage 是发送给 LLM 和从 LLM 接收的消息
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ---------------------------------------------------------------------------
// 流式事件 (从 LLM SSE 响应解析出的事件)
// ---------------------------------------------------------------------------

type StreamEvent struct {
	Type string // "text_delta", "tool_call_start", "tool_call_delta", "done", "error"

	Text     string       // text_delta
	ToolCall *ToolCall    // tool_call (accumulated)
	Message  *ChatMessage // done: 完成的完整消息
	Usage    *Usage       // done: token 用量
	Error    error        // error
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ---------------------------------------------------------------------------
// 工具定义
// ---------------------------------------------------------------------------

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage                  // JSON Schema
	Execute     func(args json.RawMessage) string // 执行函数，返回结果文本
}

// OpenAITool 是发送给 API 的工具格式
type OpenAITool struct {
	Type     string         `json:"type"` // "function"
	Function OpenAIFunction `json:"function"`
}

type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ---------------------------------------------------------------------------
// Agent 事件 (Agent 循环发出的事件，UI 层消费)
// ---------------------------------------------------------------------------

type AgentEvent struct {
	Type string

	Text       string       // text_delta
	ToolCall   *ToolCall    // tool_exec_start
	ToolName   string       // tool_exec_start/end
	ToolResult string       // tool_exec_end
	IsError    bool         // tool_exec_end
	Message    *ChatMessage // agent_end
	Error      error        // error
}

// ---------------------------------------------------------------------------
// 工具钩子 (扩展点)
// ---------------------------------------------------------------------------

// ToolHookResult 钩子返回值
type ToolHookResult struct {
	Block  bool   // true 则阻止执行
	Reason string // 阻止原因（会作为 tool result 返回给 LLM）
}

// BeforeToolCallFunc 工具执行前的钩子函数
// 参数: 工具名称、工具调用参数原始 JSON
// 返回 nil 表示允许执行
type BeforeToolCallFunc func(toolName string, args string) *ToolHookResult

const (
	EventAgentStart    = "agent_start"
	EventTextDelta     = "text_delta"
	EventToolExecStart = "tool_exec_start"
	EventToolExecEnd   = "tool_exec_end"
	EventTurnEnd       = "turn_end"
	EventAgentEnd      = "agent_end"
	EventCompactStart  = "compact_start"
	EventCompactEnd    = "compact_end"
	EventError         = "error"
)

// ---------------------------------------------------------------------------
// 会话条目 (JSONL 持久化格式)
// ---------------------------------------------------------------------------

type SessionEntry struct {
	Type      string       `json:"type"`              // "message", "compaction"
	Message   *ChatMessage `json:"message,omitempty"`  // type=message
	Summary   string       `json:"summary,omitempty"`  // type=compaction
	Timestamp string       `json:"timestamp"`
}
