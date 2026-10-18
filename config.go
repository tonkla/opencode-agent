package main

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the bot's entire configuration. It lives in config.toml so one
// file holds every value launchd and terminals would otherwise inject.
type Config struct {
	BotToken       string `toml:"telegram_bot_token"`
	AllowedUserID  int64  `toml:"telegram_allowed_user_id"`
	WorkspaceDir   string `toml:"agent_workspace_dir"`
	AgentTimeoutMs int    `toml:"agent_timeout_ms"`
	Model          string `toml:"model"`
	ModelVariant   string `toml:"model_variant"`
}

// LoadConfig reads path once at startup and fails fast on bad values. It
// names every missing key in one error and rejects unknown keys so typos
// never silently become defaults.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for _, k := range unknown {
			names = append(names, k.String())
		}
		return Config{}, fmt.Errorf("unknown keys in %s: %s", path, strings.Join(names, ", "))
	}
	var missing []string
	if cfg.BotToken == "" {
		missing = append(missing, "telegram_bot_token")
	}
	if cfg.AllowedUserID == 0 {
		missing = append(missing, "telegram_allowed_user_id")
	}
	if cfg.WorkspaceDir == "" {
		missing = append(missing, "agent_workspace_dir")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing keys in %s: %s (copy config.toml.example to config.toml and fill in the values)",
			path, strings.Join(missing, ", "))
	}
	if cfg.AgentTimeoutMs == 0 {
		cfg.AgentTimeoutMs = 300000
	} else if cfg.AgentTimeoutMs < 0 {
		return Config{}, fmt.Errorf("agent_timeout_ms must be a positive integer")
	}
	if cfg.Model == "" {
		return Config{}, fmt.Errorf("model is required: this agent only runs OpenCode Zen models")
	}
	return cfg, nil
}
