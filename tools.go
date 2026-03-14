package main

// =============================================================================
// tools.go — 内置工具定义与执行
//
// 每个工具:
//   1. 定义 JSON Schema 参数
//   2. 实现 Execute 函数
//   3. 返回纯文本结果（会作为 tool result 发回给 LLM）
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTools 返回默认的编码工具集: read, bash, edit, write
func DefaultTools(cwd string) []Tool {
	return []Tool{
		makeReadTool(cwd),
		makeBashTool(cwd),
		makeEditTool(cwd),
		makeWriteTool(cwd),
	}
}

// ToOpenAITools 将工具转换为 OpenAI API 格式
func ToOpenAITools(tools []Tool) []OpenAITool {
	result := make([]OpenAITool, len(tools))
	for i, t := range tools {
		result[i] = OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		}
	}
	return result
}

// FindTool 按名称查找工具
func FindTool(tools []Tool, name string) *Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// read — 读取文件内容
// ---------------------------------------------------------------------------

func makeReadTool(cwd string) Tool {
	return Tool{
		Name:        "read",
		Description: "Read the contents of a file. Use this to examine files before editing.",
		Parameters: mustJSON(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file to read (relative to working directory or absolute)"
				}
			},
			"required": ["file_path"]
		}`),
		Execute: func(args json.RawMessage) string {
			var p struct {
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "Error: " + err.Error()
			}

			path := resolvePath(cwd, p.FilePath)
			data, err := os.ReadFile(path)
			if err != nil {
				return "Error: " + err.Error()
			}

			content := string(data)
			const maxLines = 500
			lines := strings.Split(content, "\n")
			if len(lines) > maxLines {
				content = strings.Join(lines[:maxLines], "\n")
				content += fmt.Sprintf("\n\n[Truncated: showing %d of %d lines]", maxLines, len(lines))
			}

			return content
		},
	}
}

// ---------------------------------------------------------------------------
// bash — 执行 shell 命令
// ---------------------------------------------------------------------------

func makeBashTool(cwd string) Tool {
	return Tool{
		Name:        "bash",
		Description: "Execute a bash command and return its output. Use for running builds, tests, git, etc.",
		Parameters: mustJSON(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The bash command to execute"
				}
			},
			"required": ["command"]
		}`),
		Execute: func(args json.RawMessage) string {
			var p struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "Error: " + err.Error()
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, "bash", "-c", p.Command)
			cmd.Dir = cwd

			output, err := cmd.CombinedOutput()
			result := string(output)

			const maxBytes = 32 * 1024
			if len(result) > maxBytes {
				result = result[:maxBytes] + "\n\n[Output truncated]"
			}

			if err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					result += "\n\n[Command timed out after 120s]"
				} else {
					result += "\n\nExit: " + err.Error()
				}
			}

			return result
		},
	}
}

// ---------------------------------------------------------------------------
// edit — 精确编辑文件 (查找并替换)
// ---------------------------------------------------------------------------

func makeEditTool(cwd string) Tool {
	return Tool{
		Name:        "edit",
		Description: "Make a surgical edit to a file. Finds the exact old_text and replaces it with new_text. You MUST read the file first to get the exact text.",
		Parameters: mustJSON(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file to edit"
				},
				"old_text": {
					"type": "string",
					"description": "The exact text to find (must match exactly)"
				},
				"new_text": {
					"type": "string",
					"description": "The replacement text"
				}
			},
			"required": ["file_path", "old_text", "new_text"]
		}`),
		Execute: func(args json.RawMessage) string {
			var p struct {
				FilePath string `json:"file_path"`
				OldText  string `json:"old_text"`
				NewText  string `json:"new_text"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "Error: " + err.Error()
			}

			path := resolvePath(cwd, p.FilePath)
			data, err := os.ReadFile(path)
			if err != nil {
				return "Error: " + err.Error()
			}

			content := string(data)
			if !strings.Contains(content, p.OldText) {
				return "Error: old_text not found in file. Make sure it matches exactly (use read first)."
			}

			newContent := strings.Replace(content, p.OldText, p.NewText, 1)
			if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
				return "Error: " + err.Error()
			}

			return fmt.Sprintf("Edited %s", p.FilePath)
		},
	}
}

// ---------------------------------------------------------------------------
// write — 创建或覆盖文件
// ---------------------------------------------------------------------------

func makeWriteTool(cwd string) Tool {
	return Tool{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file. Use edit for partial changes.",
		Parameters: mustJSON(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file to write"
				},
				"content": {
					"type": "string",
					"description": "The full content to write"
				}
			},
			"required": ["file_path", "content"]
		}`),
		Execute: func(args json.RawMessage) string {
			var p struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "Error: " + err.Error()
			}

			path := resolvePath(cwd, p.FilePath)

			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "Error creating directory: " + err.Error()
			}

			if err := os.WriteFile(path, []byte(p.Content), 0644); err != nil {
				return "Error: " + err.Error()
			}

			return fmt.Sprintf("Wrote %s (%d bytes)", p.FilePath, len(p.Content))
		},
	}
}

// ---------------------------------------------------------------------------
// 工具函数
// ---------------------------------------------------------------------------

func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwd, path)
}

func mustJSON(s string) json.RawMessage {
	var v json.RawMessage
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic("invalid JSON: " + err.Error())
	}
	return v
}
