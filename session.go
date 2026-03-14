package main

// =============================================================================
// session.go — 会话持久化与上下文压缩
//
// 存储结构:
//   .tiny-ai-agent/sessions/
//     ├── 20260314T100000Z/session.jsonl   ← 按时间戳命名的独立会话
//     ├── 20260314T113000Z/session.jsonl
//     └── ...
//
// 存储格式: JSONL (每行一条 JSON)
//   {"type":"message","message":{...},"timestamp":"2026-03-14T10:00:00Z"}
//   {"type":"compaction","summary":"...","timestamp":"..."}
//
// 关键设计:
//   - 每次启动创建新的时间戳目录
//   - --continue 恢复最近一个会话
//   - 追加写入，永不修改已写入的行
//   - compaction 时保留最近 N tokens 的消息，旧消息用 LLM 生成摘要替换
// =============================================================================

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session 管理单个会话的持久化
type Session struct {
	dir  string // 会话目录 (如 sessions/20260314T100000Z)
	path string // JSONL 文件路径
}

// CreateSession 创建新会话（懒创建：目录在首次写入时才实际创建）
func CreateSession(sessionsDir string) (*Session, error) {
	name := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(sessionsDir, name)
	return &Session{
		dir:  dir,
		path: filepath.Join(dir, "session.jsonl"),
	}, nil
}

// LatestSession 找到 sessionsDir 下最近的会话
// 如果没有任何会话，返回 nil, nil
func LatestSession(sessionsDir string) (*Session, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// 过滤出有 session.jsonl 的目录
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		jsonlPath := filepath.Join(sessionsDir, e.Name(), "session.jsonl")
		if info, err := os.Stat(jsonlPath); err == nil && info.Size() > 0 {
			dirs = append(dirs, e.Name())
		}
	}

	if len(dirs) == 0 {
		return nil, nil
	}

	// 时间戳目录名天然可排序，取最后一个
	sort.Strings(dirs)
	latest := dirs[len(dirs)-1]
	dir := filepath.Join(sessionsDir, latest)

	return &Session{
		dir:  dir,
		path: filepath.Join(dir, "session.jsonl"),
	}, nil
}

// SessionInfo 会话摘要信息（用于列表展示）
type SessionInfo struct {
	Name     string // 目录名 (时间戳)
	Dir      string // 完整目录路径
	Messages int    // 消息数量
	Preview  string // 首条用户消息预览
}

// ListSessions 列出 sessionsDir 下所有会话，按时间倒序
func ListSessions(sessionsDir string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []SessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		jsonlPath := filepath.Join(sessionsDir, e.Name(), "session.jsonl")
		info, err := os.Stat(jsonlPath)
		if err != nil || info.Size() == 0 {
			continue
		}

		// 快速扫描: 统计消息数，提取首条用户消息
		msgCount, preview := scanSessionPreview(jsonlPath)
		if msgCount == 0 {
			continue
		}

		sessions = append(sessions, SessionInfo{
			Name:     e.Name(),
			Dir:      filepath.Join(sessionsDir, e.Name()),
			Messages: msgCount,
			Preview:  preview,
		})
	}

	// 倒序: 最新的在前
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Name > sessions[j].Name
	})

	return sessions, nil
}

// OpenSession 打开指定目录的已有会话
func OpenSession(dir string) *Session {
	return &Session{
		dir:  dir,
		path: filepath.Join(dir, "session.jsonl"),
	}
}

// scanSessionPreview 快速扫描 JSONL 文件，返回消息数和首条用户消息预览
func scanSessionPreview(path string) (int, string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	count := 0
	preview := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry SessionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "message" && entry.Message != nil {
			count++
			if preview == "" && entry.Message.Role == "user" {
				preview = entry.Message.Content
				if len(preview) > 80 {
					preview = preview[:80] + "..."
				}
			}
		}
	}
	return count, preview
}

// Append 追加一条会话条目（首次写入时自动创建目录）
func (s *Session) Append(entry SessionEntry) error {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(append(data, '\n'))
	return err
}

// AppendMessage 追加一条消息
func (s *Session) AppendMessage(msg ChatMessage) error {
	return s.Append(SessionEntry{
		Type:    "message",
		Message: &msg,
	})
}

// AppendCompaction 追加一条压缩摘要
func (s *Session) AppendCompaction(summary string) error {
	return s.Append(SessionEntry{
		Type:    "compaction",
		Summary: summary,
	})
}

// Load 从 JSONL 文件恢复消息
// 处理逻辑:
//  1. 读取所有条目
//  2. 如果遇到 compaction，丢弃之前的所有消息，用 summary 替换
//  3. 后续的 message 条目直接追加
func (s *Session) Load() ([]ChatMessage, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []SessionEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry SessionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // 跳过损坏的行
		}
		entries = append(entries, entry)
	}

	// 重建消息: 从最后一个 compaction 开始
	var messages []ChatMessage
	for _, entry := range entries {
		switch entry.Type {
		case "compaction":
			messages = []ChatMessage{{
				Role:    "user",
				Content: "The conversation history before this point was compacted into the following summary:\n\n<summary>\n" + entry.Summary + "\n</summary>",
			}}
		case "message":
			if entry.Message != nil {
				messages = append(messages, *entry.Message)
			}
		}
	}

	return messages, nil
}

// HasData 检查是否有已保存的会话数据
func (s *Session) HasData() bool {
	info, err := os.Stat(s.path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// Dir 返回会话目录名（用于显示）
func (s *Session) Dir() string {
	return filepath.Base(s.dir)
}

// Path 返回会话文件路径
func (s *Session) Path() string {
	return s.path
}

// =============================================================================
// 上下文压缩 (Compaction)
//
// 触发条件: 估计 token 数 > contextWindow - reserveTokens
// 处理流程:
//   1. 从后往前保留 keepRecentTokens 的消息
//   2. 用 LLM 对旧消息生成摘要
//   3. 替换旧消息为摘要
// =============================================================================

const (
	defaultContextWindow = 128000 // 默认上下文窗口大小
	defaultReserveTokens = 16384  // 预留 token 空间
	defaultKeepTokens    = 20000  // 压缩时保留的最近消息 token 数
)

// EstimateTokens 估算消息的 token 数 (chars/4 启发式)
func EstimateTokens(messages []ChatMessage) int {
	total := 0
	for _, m := range messages {
		chars := len(m.Content)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		total += chars / 4
	}
	return total
}

// ShouldCompact 检查是否需要压缩
func ShouldCompact(messages []ChatMessage) bool {
	tokens := EstimateTokens(messages)
	return tokens > defaultContextWindow-defaultReserveTokens
}

// Compact 执行上下文压缩
func Compact(
	ctx context.Context,
	messages []ChatMessage,
	baseURL, apiKey, model string,
) (summary string, keptMessages []ChatMessage, err error) {
	if len(messages) < 4 {
		return "", messages, nil
	}

	// 从后往前找切割点
	cutIndex := len(messages)
	keptTokens := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msgTokens := len(messages[i].Content) / 4
		for _, tc := range messages[i].ToolCalls {
			msgTokens += (len(tc.Function.Name) + len(tc.Function.Arguments)) / 4
		}
		if keptTokens+msgTokens > defaultKeepTokens {
			break
		}
		keptTokens += msgTokens
		cutIndex = i
	}

	if cutIndex <= 1 {
		return "", messages, nil
	}

	// 序列化旧消息
	oldMessages := messages[:cutIndex]
	var sb strings.Builder
	for _, m := range oldMessages {
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("  -> tool_call: %s(%s)\n", tc.Function.Name, tc.Function.Arguments))
		}
	}

	// 调用 LLM 生成摘要
	summaryPrompt := []ChatMessage{
		{
			Role:    "system",
			Content: "You are a summarizer. Summarize the following conversation concisely, preserving key decisions, file changes, and important context. Be thorough but concise.",
		},
		{
			Role:    "user",
			Content: "Summarize this conversation:\n\n" + sb.String(),
		},
	}

	summary, err = CompleteChat(ctx, baseURL, apiKey, model, summaryPrompt)
	if err != nil {
		return "", messages, fmt.Errorf("compaction LLM call failed: %w", err)
	}

	keptMessages = messages[cutIndex:]

	return summary, keptMessages, nil
}
