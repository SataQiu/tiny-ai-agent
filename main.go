package main

// =============================================================================
// main.go — CLI 入口与交互循环
//
// 使用方式:
//   go run . [options] [message...]
//
// 选项:
//   --url      LLM API 地址 (默认 http://0.0.0.0:4000)
//   --model    模型名称 (默认 gpt-4o)
//   --api-key  API Key (也可用 OPENAI_API_KEY 环境变量)
//   --continue 继续上次会话
//   -p         单次输出模式 (非交互)
// =============================================================================

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chzyer/readline"
)

// ANSI 颜色
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
)


func main() {
	// CLI 参数解析
	baseURL := flag.String("url", "http://0.0.0.0:4000", "LLM API base URL")
	model := flag.String("model", "gpt-4o", "Model name")
	apiKey := flag.String("api-key", "", "API key (or set OPENAI_API_KEY)")
	cont := flag.Bool("continue", false, "Continue previous session")
	printMode := flag.Bool("p", false, "Print mode (non-interactive, single response)")
	flag.Parse()

	cwd, _ := os.Getwd()

	// 加载配置文件 (优先级: 命令行 > 环境变量 > 项目级配置 > 用户级配置)
	fileCfg := LoadConfig(cwd)

	if *baseURL == "http://0.0.0.0:4000" && fileCfg.BaseURL != "" {
		*baseURL = fileCfg.BaseURL
	}
	if *model == "gpt-4o" && fileCfg.Model != "" {
		*model = fileCfg.Model
	}
	if *apiKey == "" {
		*apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if *apiKey == "" {
		*apiKey = fileCfg.APIKey
	}

	// 会话管理
	sessionsDir := filepath.Join(cwd, ".tiny-ai-agent", "sessions")
	var session *Session
	var err error
	if *cont {
		session, err = LatestSession(sessionsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to find session: %s\n", err)
		}
		if session == nil {
			fmt.Fprintf(os.Stderr, "Warning: no previous session found, starting new\n")
		}
	}
	if session == nil {
		session, err = CreateSession(sessionsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: session disabled: %s\n", err)
		}
	}

	// 创建 Agent
	config := AgentConfig{
		BaseURL: *baseURL,
		APIKey:  *apiKey,
		Model:   *model,
		CWD:     cwd,
	}

	// readline: 支持上下箭头历史、行编辑 (从 session 的 user 消息加载历史)
	newReadline := func(s *Session) *readline.Instance {
		inst, err := readline.NewEx(&readline.Config{
			Prompt:          colorGreen + "> " + colorReset,
			InterruptPrompt: "^C",
			EOFPrompt:       "exit",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: readline disabled: %s\n", err)
			return nil
		}
		// 从 session 恢复用户输入历史
		if s != nil {
			if msgs, err := s.Load(); err == nil {
				for _, m := range msgs {
					if m.Role == "user" && !strings.HasPrefix(m.Content, "The conversation history before") {
						inst.SaveHistory(m.Content)
					}
				}
			}
		}
		return inst
	}
	rl := newReadline(session)
	if rl != nil {
		defer rl.Close()
	}

	agent := NewAgent(config, session, handleEvent)

	// 工具确认钩子 (默认需确认，配置 auto_approve:true 跳过)
	autoApprove := fileCfg.AutoApprove != nil && *fileCfg.AutoApprove
	var confirmHook BeforeToolCallFunc
	if !autoApprove {
		confirmHook = func(toolName string, args string) *ToolHookResult {
			display := args
			if len(display) > 200 {
				display = display[:200] + "..."
			}
			fmt.Fprintf(os.Stderr, "%s[confirm] %s%s %s%s%s\n",
				colorYellow, toolName, colorReset, colorDim, display, colorReset)

			// 临时切换 prompt 用于确认
			if rl != nil {
				rl.SetPrompt(colorYellow + "Allow? [Y/n]: " + colorReset)
				line, err := rl.Readline()
				rl.SetPrompt(colorGreen + "> " + colorReset)
				if err != nil {
					return &ToolHookResult{Block: true, Reason: "input error"}
				}
				input := strings.TrimSpace(strings.ToLower(line))
				if input == "n" || input == "no" {
					return &ToolHookResult{Block: true, Reason: "rejected by user"}
				}
			}
			return nil
		}
		agent.SetBeforeToolCall(confirmHook)
	}

	// 显示启动信息
	if agent.HasSession() && *cont {
		fmt.Printf("%s[Resumed session with %d messages]%s\n", colorDim, len(agent.Messages()), colorReset)
	}
	fmt.Printf("%s%stiny-ai-agent%s %s(model: %s, tools: read/bash/edit/write)%s\n",
		colorBold, colorCyan, colorReset, colorDim, *model, colorReset)
	fmt.Printf("%sType your message. Press Ctrl+C to abort, Ctrl+D to exit.%s\n\n", colorDim, colorReset)

	// 单次输出模式
	if *printMode || flag.NArg() > 0 {
		message := strings.Join(flag.Args(), " ")
		if message == "" {
			fmt.Fprintln(os.Stderr, "Error: no message provided")
			os.Exit(1)
		}
		if err := agent.Prompt(message); err != nil {
			fmt.Fprintf(os.Stderr, "\nError: %s\n", err)
			os.Exit(1)
		}
		fmt.Println()
		return
	}

	// Ctrl+C: 中断当前 LLM 调用
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		for range sigCh {
			agent.Abort()
			fmt.Printf("\n%s[Aborted]%s\n", colorYellow, colorReset)
		}
	}()

	// 交互循环
	for {
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt {
				continue
			}
			if err == io.EOF {
				break
			}
			break
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		// 特殊命令
		if input == "/quit" || input == "/exit" {
			break
		}
		if input == "/messages" {
			msgs := agent.Messages()
			fmt.Printf("%s[%d messages, ~%d tokens]%s\n", colorDim, len(msgs), EstimateTokens(msgs), colorReset)
			continue
		}
		if input == "/clear" {
			newSession, err := CreateSession(sessionsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%sError creating session: %s%s\n", colorYellow, err, colorReset)
				continue
			}
			session = newSession
			agent = NewAgent(config, session, handleEvent)
			if !autoApprove {
				agent.SetBeforeToolCall(confirmHook)
			}
			if rl != nil {
				rl.Close()
			}
			rl = newReadline(session)
			fmt.Printf("%s[New session: %s]%s\n", colorDim, session.Dir(), colorReset)
			continue
		}
		if input == "/resume" {
			list, err := ListSessions(sessionsDir)
			if err != nil || len(list) == 0 {
				fmt.Printf("%s[No sessions found]%s\n", colorDim, colorReset)
				continue
			}
			limit := len(list)
			if limit > 10 {
				limit = 10
			}
			fmt.Printf("%sSessions (newest first):%s\n", colorBold, colorReset)
			for i, s := range list[:limit] {
				current := ""
				if s.Name == session.Dir() {
					current = colorGreen + " ← current" + colorReset
				}
				fmt.Printf("  %s%d)%s %s %s(%d msgs)%s %s%s%s%s\n",
					colorCyan, i+1, colorReset,
					s.Name,
					colorDim, s.Messages, colorReset,
					colorDim, s.Preview, colorReset,
					current)
			}
			rl.SetPrompt(fmt.Sprintf("%sSelect [1-%d] or Enter to cancel: %s", colorYellow, limit, colorReset))
			choice, err := rl.Readline()
			rl.SetPrompt(colorGreen + "> " + colorReset)
			if err != nil || strings.TrimSpace(choice) == "" {
				continue
			}
			var idx int
			if _, err := fmt.Sscanf(strings.TrimSpace(choice), "%d", &idx); err != nil || idx < 1 || idx > limit {
				fmt.Printf("%s[Invalid selection]%s\n", colorYellow, colorReset)
				continue
			}
			selected := list[idx-1]
			session = OpenSession(selected.Dir)
			agent = NewAgent(config, session, handleEvent)
			if !autoApprove {
				agent.SetBeforeToolCall(confirmHook)
			}
			if rl != nil {
				rl.Close()
			}
			rl = newReadline(session)
			fmt.Printf("%s[Resumed session: %s, %d messages]%s\n",
				colorDim, selected.Name, len(agent.Messages()), colorReset)
			continue
		}
		if input == "/help" {
			fmt.Printf("%sCommands:%s\n", colorBold, colorReset)
			fmt.Printf("  /messages  Show message count and token estimate\n")
			fmt.Printf("  /clear     New session (clear history)\n")
			fmt.Printf("  /resume    List and resume a previous session\n")
			fmt.Printf("  /quit      Exit\n")
			fmt.Printf("  /help      Show this help\n")
			continue
		}

		// 调用 Agent
		if err := agent.Prompt(input); err != nil {
			if err.Error() != "context canceled" {
				fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorYellow, err, colorReset)
			}
		}
		fmt.Println()
	}

	fmt.Printf("\n%sBye!%s\n", colorDim, colorReset)
}

// handleEvent 处理 Agent 事件，渲染到终端
func handleEvent(event AgentEvent) {
	switch event.Type {
	case EventAgentStart:

	case EventTextDelta:
		fmt.Print(event.Text)

	case EventToolExecStart:
		args := ""
		if event.ToolCall != nil {
			args = event.ToolCall.Function.Arguments
		}
		if len(args) > 200 {
			args = args[:200] + "..."
		}
		fmt.Printf("\n%s[tool] %s%s %s%s%s\n",
			colorCyan, event.ToolName, colorReset,
			colorDim, args, colorReset)

	case EventToolExecEnd:
		result := event.ToolResult
		lines := strings.Split(result, "\n")
		if len(lines) > 15 {
			result = strings.Join(lines[:15], "\n") +
				fmt.Sprintf("\n  ... (%d more lines)", len(lines)-15)
		}
		prefix := colorDim
		if event.IsError {
			prefix = colorYellow
		}
		fmt.Printf("%s  %s%s\n", prefix, result, colorReset)

	case EventTurnEnd:
	case EventAgentEnd:

	case EventCompactStart:
		fmt.Printf("\n%s[Compacting context...]%s\n", colorDim, colorReset)

	case EventCompactEnd:
		fmt.Printf("%s[Context compacted]%s\n", colorDim, colorReset)

	case EventError:
		if event.Error != nil {
			fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorYellow, event.Error, colorReset)
		}
	}
}
