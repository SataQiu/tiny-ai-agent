# tiny-ai-agent

一个极简的终端 AI 编码助手，用 Go 实现，约 800 行代码。

通过 OpenAI 兼容 API（如 LiteLLM Proxy）与 LLM 交互，支持流式输出、工具调用、会话持久化和上下文压缩。

## 快速开始

```bash
# 编译
go build -o tiny-ai-agent .

# 运行（需要先启动 LiteLLM Proxy 或配置 API 地址）
./tiny-ai-agent

# 带参数运行
./tiny-ai-agent --url http://0.0.0.0:4000 --model gpt-4o --api-key sk-xxx

# 继续上次会话
./tiny-ai-agent --continue

# 单次调用（非交互模式）
./tiny-ai-agent -p "解释这个项目的目录结构"
```

## 配置

支持配置文件，优先级从高到低：

1. 命令行参数（`--url`, `--model`, `--api-key`）
2. 环境变量（`OPENAI_API_KEY`）
3. 项目级配置 `.tiny-ai-agent/config.json`
4. 用户级配置 `~/.tiny-ai-agent/config.json`

配置文件示例：

```json
{
  "url": "http://0.0.0.0:4000",
  "model": "gpt-4o",
  "api_key": "sk-xxx",
  "auto_approve": false
}
```

| 字段 | 说明 | 默认值 |
|------|------|--------|
| `url` | LLM API 地址 | `http://0.0.0.0:4000` |
| `model` | 模型名称 | `gpt-4o` |
| `api_key` | API Key | 无 |
| `auto_approve` | 跳过工具执行确认 | `false`（每次执行前需确认） |

## 内置工具

| 工具 | 说明 |
|------|------|
| `read` | 读取文件内容（超过 500 行自动截断） |
| `bash` | 执行 shell 命令（120 秒超时，32KB 输出限制） |
| `edit` | 精确查找替换编辑文件 |
| `write` | 创建新文件或完全覆写文件 |

默认情况下，每次工具执行前会弹出确认提示（`Allow? [Y/n]`）。设置 `auto_approve: true` 可跳过确认。

## 交互命令

| 命令 | 说明 |
|------|------|
| `/messages` | 显示当前消息数和 token 估算 |
| `/clear` | 新建会话（清空历史） |
| `/resume` | 列出历史会话并选择恢复 |
| `/help` | 显示帮助信息 |
| `/quit` | 退出 |

快捷键：
- `Ctrl+C` — 中断当前 LLM 调用
- `Ctrl+D` — 退出程序
- `↑/↓` — 滚动当前会话的用户输入历史

## 会话管理

会话数据存储在 `.tiny-ai-agent/sessions/` 下，每个会话一个时间戳目录：

```
.tiny-ai-agent/sessions/
├── 20260314T100000Z/session.jsonl
├── 20260314T113000Z/session.jsonl
└── ...
```

- 格式为 JSONL，追加写入，不修改已写入的内容
- 会话目录懒创建，只有实际发送消息时才会生成
- `--continue` 自动恢复最近一个会话
- `/resume` 可以列出并选择历史会话

### 上下文压缩

当消息估计超过约 112K tokens 时自动触发压缩：

1. 保留最近约 20K tokens 的消息
2. 用 LLM 对旧消息生成摘要
3. 摘要替换旧消息，写入 session 文件

## 架构

```
main.go     CLI 入口、readline 交互循环、工具确认钩子
agent.go    Agent 核心循环：LLM 调用 → 工具执行 → 重复
llm.go      OpenAI 兼容的 SSE 流式客户端
tools.go    内置工具（read/bash/edit/write）
prompt.go   系统提示构建
session.go  会话持久化（JSONL）与上下文压缩
config.go   配置文件加载
types.go    共享类型定义
```

核心流程：

```
用户输入 → agent.Prompt()
            → 追加 user 消息
            → runLoop:
                LLM 流式调用 → 解析响应
                有 tool_calls? → 执行工具 → 结果加入消息 → 继续循环
                无 tool_calls? → 结束
            → 检查是否需要上下文压缩
            → 持久化到 session
```

## 依赖

- [github.com/chzyer/readline](https://github.com/chzyer/readline) — 终端行编辑和输入历史
- Go 1.25+
