package main

// =============================================================================
// agent.go — Agent 核心循环
//
// 核心流程:
//
//   Prompt(text)
//     → messages = append(messages, userMessage)
//     → runLoop:
//         LLM 调用 → 检查 tool_calls → 执行工具 → 结果加入 messages → 重复
//         直到无工具调用 → 结束
//     → checkCompaction()
//     → 持久化到 session
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentConfig Agent 配置
type AgentConfig struct {
	BaseURL string // LLM API 地址
	APIKey  string // API Key
	Model   string // 模型名称
	CWD     string // 工作目录
}

// Agent 是核心状态机，管理消息、工具和 LLM 交互
type Agent struct {
	config  AgentConfig
	tools   []Tool
	session *Session

	messages     []ChatMessage
	systemPrompt string

	onEvent        func(AgentEvent)
	beforeToolCall BeforeToolCallFunc
	cancel         context.CancelFunc
}

// SetBeforeToolCall 注册工具执行前的钩子
func (a *Agent) SetBeforeToolCall(hook BeforeToolCallFunc) {
	a.beforeToolCall = hook
}

// NewAgent 创建新的 Agent
func NewAgent(config AgentConfig, session *Session, onEvent func(AgentEvent)) *Agent {
	tools := DefaultTools(config.CWD)

	a := &Agent{
		config:  config,
		tools:   tools,
		session: session,
		onEvent: onEvent,
	}

	a.systemPrompt = BuildSystemPrompt(tools, config.CWD)

	// 从 session 恢复消息
	if session != nil {
		msgs, err := session.Load()
		if err == nil && len(msgs) > 0 {
			a.messages = msgs
		}
	}

	return a
}

// Prompt 发送用户消息并运行完整的 Agent 循环
func (a *Agent) Prompt(text string) error {
	userMsg := ChatMessage{
		Role:    "user",
		Content: text,
	}
	a.messages = append(a.messages, userMsg)

	if a.session != nil {
		a.session.AppendMessage(userMsg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	defer func() { a.cancel = nil }()

	a.emit(AgentEvent{Type: EventAgentStart})

	err := a.runLoop(ctx)

	a.emit(AgentEvent{Type: EventAgentEnd})

	a.checkCompaction(ctx)

	return err
}

// Abort 中断当前运行
func (a *Agent) Abort() {
	if a.cancel != nil {
		a.cancel()
	}
}

// Messages 返回当前消息列表
func (a *Agent) Messages() []ChatMessage {
	return a.messages
}

// HasSession 检查是否有历史会话
func (a *Agent) HasSession() bool {
	return len(a.messages) > 0
}

// ---------------------------------------------------------------------------
// 核心循环
//
// LLM 调用 → 工具执行 → 重复直到无工具调用
//
// 一次 prompt 可能触发多轮 LLM 调用：
//   第 1 轮: LLM 返回 "我来读取文件" + tool_call(read)
//   执行 read → 结果加入消息
//   第 2 轮: LLM 收到文件内容，返回 "这个文件包含..." (无 tool_call → 结束)
// ---------------------------------------------------------------------------

func (a *Agent) runLoop(ctx context.Context) error {
	for {
		// 步骤 1: 调用 LLM (流式)
		apiMessages := PrepareMessages(a.systemPrompt, a.messages)
		openAITools := ToOpenAITools(a.tools)

		events := StreamChat(ctx, a.config.BaseURL, a.config.APIKey, a.config.Model, apiMessages, openAITools)

		var assistantMsg *ChatMessage
		for event := range events {
			switch event.Type {
			case "text_delta":
				a.emit(AgentEvent{Type: EventTextDelta, Text: event.Text})

			case "done":
				assistantMsg = event.Message

			case "error":
				a.emit(AgentEvent{Type: EventError, Error: event.Error})
				return event.Error
			}
		}

		if assistantMsg == nil {
			return fmt.Errorf("no response from LLM")
		}

		// 将 assistant 消息加入历史
		a.messages = append(a.messages, *assistantMsg)
		if a.session != nil {
			a.session.AppendMessage(*assistantMsg)
		}

		// 步骤 2: 检查是否有工具调用
		if len(assistantMsg.ToolCalls) == 0 {
			a.emit(AgentEvent{Type: EventTurnEnd})
			return nil
		}

		// 步骤 3: 顺序执行工具调用
		for _, tc := range assistantMsg.ToolCalls {
			a.emit(AgentEvent{
				Type:     EventToolExecStart,
				ToolCall: &tc,
				ToolName: tc.Function.Name,
			})

			var result string
			var isError bool

			// 钩子: 执行前检查
			if a.beforeToolCall != nil {
				if hookResult := a.beforeToolCall(tc.Function.Name, tc.Function.Arguments); hookResult != nil && hookResult.Block {
					result = "Blocked: " + hookResult.Reason
					isError = true

					a.emit(AgentEvent{
						Type:       EventToolExecEnd,
						ToolName:   tc.Function.Name,
						ToolResult: result,
						IsError:    isError,
					})

					toolResultMsg := ChatMessage{
						Role:       "tool",
						Content:    result,
						ToolCallID: tc.ID,
					}
					a.messages = append(a.messages, toolResultMsg)
					if a.session != nil {
						a.session.AppendMessage(toolResultMsg)
					}
					continue
				}
			}

			tool := FindTool(a.tools, tc.Function.Name)

			if tool == nil {
				result = fmt.Sprintf("Error: tool %q not found", tc.Function.Name)
				isError = true
			} else {
				var args json.RawMessage
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing arguments: %s", err)
					isError = true
				} else {
					result = tool.Execute(args)
					isError = false
				}
			}

			a.emit(AgentEvent{
				Type:       EventToolExecEnd,
				ToolName:   tc.Function.Name,
				ToolResult: result,
				IsError:    isError,
			})

			toolResultMsg := ChatMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			}
			a.messages = append(a.messages, toolResultMsg)
			if a.session != nil {
				a.session.AppendMessage(toolResultMsg)
			}
		}

		a.emit(AgentEvent{Type: EventTurnEnd})

		// 工具结果已加入消息，回到步骤 1 继续调用 LLM
	}
}

// ---------------------------------------------------------------------------
// 上下文压缩
// 在每次 agent 循环结束后检查，如果 token 超过阈值则触发压缩
// ---------------------------------------------------------------------------

func (a *Agent) checkCompaction(ctx context.Context) {
	if !ShouldCompact(a.messages) {
		return
	}

	a.emit(AgentEvent{Type: EventCompactStart})

	summary, keptMessages, err := Compact(
		ctx,
		a.messages,
		a.config.BaseURL, a.config.APIKey, a.config.Model,
	)

	if err != nil {
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("compaction failed: %w", err)})
		return
	}

	if summary == "" {
		return
	}

	summaryMsg := ChatMessage{
		Role:    "user",
		Content: "The conversation history before this point was compacted into the following summary:\n\n<summary>\n" + summary + "\n</summary>",
	}
	a.messages = append([]ChatMessage{summaryMsg}, keptMessages...)

	if a.session != nil {
		a.session.AppendCompaction(summary)
	}

	a.emit(AgentEvent{Type: EventCompactEnd})
}

func (a *Agent) emit(event AgentEvent) {
	if a.onEvent != nil {
		a.onEvent(event)
	}
}
