package main

// =============================================================================
// llm.go — OpenAI 兼容的流式 LLM 客户端
//
// 使用 Go channel 作为流式事件的传输机制。
// =============================================================================

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// API 请求/响应结构
// ---------------------------------------------------------------------------

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []OpenAITool  `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string          `json:"role,omitempty"`
			Content   string          `json:"content,omitempty"`
			ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

// toolCallDelta 是流式工具调用的增量片段
// LLM 将 tool_calls 的 name 和 arguments 拆分成多个 delta 发送
type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// ---------------------------------------------------------------------------
// StreamChat — 流式调用 LLM
// 返回一个 event channel，调用方 range 消费即可。
// ---------------------------------------------------------------------------

func StreamChat(
	ctx context.Context,
	baseURL, apiKey, model string,
	messages []ChatMessage,
	tools []OpenAITool,
) <-chan StreamEvent {
	ch := make(chan StreamEvent, 64)

	go func() {
		defer close(ch)

		reqBody := chatRequest{
			Model:    model,
			Messages: messages,
			Stream:   true,
		}
		if len(tools) > 0 {
			reqBody.Tools = tools
		}

		body, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: fmt.Errorf("marshal request: %w", err)}
			return
		}

		req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: fmt.Errorf("create request: %w", err)}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- StreamEvent{Type: "error", Error: fmt.Errorf("http request: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			errBody, _ := io.ReadAll(resp.Body)
			ch <- StreamEvent{Type: "error", Error: fmt.Errorf("API error %d: %s", resp.StatusCode, string(errBody))}
			return
		}

		parseSSEStream(resp.Body, ch)
	}()

	return ch
}

// ---------------------------------------------------------------------------
// SSE 解析 + Tool Call 累积
//
// OpenAI 流式格式:
//   data: {"choices":[{"delta":{"content":"Hello"}}]}
//   data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_xx","function":{"name":"read"}}]}}]}
//   data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file"}}]}}]}
//   data: [DONE]
//
// 关键: tool_calls 的 arguments 被拆分成多个 delta，需要累积拼接。
// ---------------------------------------------------------------------------

func parseSSEStream(body io.Reader, ch chan<- StreamEvent) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fullContent := strings.Builder{}
	var accumulatedToolCalls []ToolCall
	var toolCallBuilders []struct {
		id   string
		name string
		args strings.Builder
	}
	var lastUsage *Usage

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			fullContent.WriteString(delta.Content)
			ch <- StreamEvent{Type: "text_delta", Text: delta.Content}
		}

		// 处理工具调用 delta
		for _, tc := range delta.ToolCalls {
			idx := tc.Index

			for len(toolCallBuilders) <= idx {
				toolCallBuilders = append(toolCallBuilders, struct {
					id   string
					name string
					args strings.Builder
				}{})
			}

			if tc.ID != "" {
				toolCallBuilders[idx].id = tc.ID
			}
			if tc.Function.Name != "" {
				toolCallBuilders[idx].name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				toolCallBuilders[idx].args.WriteString(tc.Function.Arguments)
			}
		}
	}

	// 从 builders 构建最终的 tool calls
	for _, b := range toolCallBuilders {
		tc := ToolCall{
			ID:   b.id,
			Type: "function",
			Function: FunctionCall{
				Name:      b.name,
				Arguments: b.args.String(),
			},
		}
		accumulatedToolCalls = append(accumulatedToolCalls, tc)
	}

	finalMsg := &ChatMessage{
		Role:      "assistant",
		Content:   fullContent.String(),
		ToolCalls: accumulatedToolCalls,
	}

	ch <- StreamEvent{
		Type:    "done",
		Message: finalMsg,
		Usage:   lastUsage,
	}
}

// ---------------------------------------------------------------------------
// CompleteChat — 非流式调用 (用于 compaction 摘要生成)
// ---------------------------------------------------------------------------

func CompleteChat(
	ctx context.Context,
	baseURL, apiKey, model string,
	messages []ChatMessage,
) (string, error) {
	reqBody := chatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(errBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}
