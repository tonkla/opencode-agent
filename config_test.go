package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "absent.toml"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadConfigMissingNamesAll(t *testing.T) {
	_, err := LoadConfig(writeTOML(t, ""))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"telegram_bot_token", "telegram_allowed_user_id", "agent_workspace_dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadConfigPartialNamesRest(t *testing.T) {
	_, err := LoadConfig(writeTOML(t, "telegram_bot_token = \"tok\"\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "telegram_bot_token") {
		t.Errorf("present key must not be listed: %q", err)
	}
	for _, want := range []string{"telegram_allowed_user_id", "agent_workspace_dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadConfigRejectsUnknownKeys(t *testing.T) {
	body := "telegram_bot_token = \"tok\"\ntelegram_allowed_user_id = 42\nagent_workspace_dir = \"/tmp/ws\"\nbot_tokken = \"typo\"\n"
	_, err := LoadConfig(writeTOML(t, body))
	if err == nil || !strings.Contains(err.Error(), "bot_tokken") {
		t.Errorf("expected unknown-key error, got %v", err)
	}
}

// The serve is managed internally now, so the old serve keys are unknown and
// the error names them for easy cleanup.
func TestLoadConfigRejectsRemovedServeKeys(t *testing.T) {
	body := "telegram_bot_token = \"tok\"\ntelegram_allowed_user_id = 42\nagent_workspace_dir = \"/tmp/ws\"\nmodel = \"m\"\nopencode_password = \"pw\"\nopencode_base_url = \"http://127.0.0.1:4096\"\nopencode_server_user = \"opencode\"\n"
	_, err := LoadConfig(writeTOML(t, body))
	if err == nil {
		t.Fatal("expected unknown-key error")
	}
	for _, want := range []string{"opencode_password", "opencode_base_url", "opencode_server_user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	body := "telegram_bot_token = \"tok\"\ntelegram_allowed_user_id = 42\nagent_workspace_dir = \"/tmp/ws\"\nmodel = \"opencode/deepseek-v4.1-flash-free\"\n"
	cfg, err := LoadConfig(writeTOML(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentTimeoutMs != 300000 {
		t.Errorf("bad defaults: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadTimeout(t *testing.T) {
	body := "telegram_bot_token = \"tok\"\ntelegram_allowed_user_id = 42\nagent_workspace_dir = \"/tmp/ws\"\nagent_timeout_ms = -5\nmodel = \"opencode/deepseek-v4.1-flash-free\"\n"
	if _, err := LoadConfig(writeTOML(t, body)); err == nil {
		t.Error("expected timeout error")
	}
}

func TestLoadConfigRequiresModel(t *testing.T) {
	body := "telegram_bot_token = \"tok\"\ntelegram_allowed_user_id = 42\nagent_workspace_dir = \"/tmp/ws\"\n"
	_, err := LoadConfig(writeTOML(t, body))
	if err == nil || !strings.Contains(err.Error(), "Zen") {
		t.Errorf("expected Zen-only error, got %v", err)
	}
}
