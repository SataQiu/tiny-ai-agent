package main

// =============================================================================
// config.go — 配置文件加载
//
// 优先级 (高 → 低):
//   1. 命令行参数 (--url, --model, --api-key)
//   2. 环境变量 (OPENAI_API_KEY)
//   3. 项目级配置 .tiny-ai-agent/config.json
//   4. 用户级配置 ~/.tiny-ai-agent/config.json
// =============================================================================

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// FileConfig 配置文件结构
type FileConfig struct {
	BaseURL    string `json:"url"`
	Model      string `json:"model"`
	APIKey     string `json:"api_key"`
	AutoApprove *bool `json:"auto_approve"` // nil=未设置(默认false,需确认), true=自动批准工具执行
}

// LoadConfig 按优先级加载配置文件
// 先加载用户级，再加载项目级（项目级覆盖用户级）
func LoadConfig(cwd string) FileConfig {
	var cfg FileConfig

	// 用户级: ~/.tiny-ai-agent/config.json
	if home, err := os.UserHomeDir(); err == nil {
		mergeCfg(&cfg, filepath.Join(home, ".tiny-ai-agent", "config.json"))
	}

	// 项目级: .tiny-ai-agent/config.json
	mergeCfg(&cfg, filepath.Join(cwd, ".tiny-ai-agent", "config.json"))

	return cfg
}

func mergeCfg(cfg *FileConfig, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var fc FileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return
	}
	if fc.BaseURL != "" {
		cfg.BaseURL = fc.BaseURL
	}
	if fc.Model != "" {
		cfg.Model = fc.Model
	}
	if fc.APIKey != "" {
		cfg.APIKey = fc.APIKey
	}
	if fc.AutoApprove != nil {
		cfg.AutoApprove = fc.AutoApprove
	}
}
