package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, cfg map[string]any) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshalling config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func validConfig() map[string]any {
	return map[string]any{
		"miniflux_url":     "https://rss.example.org",
		"miniflux_api_key": "secret",
		"output_dir":       "/tmp/digest",
	}
}

func TestLoadConfigDefaultsAndGenerateOnlyMode(t *testing.T) {
	cfg, err := loadConfig(writeConfigFile(t, validConfig()))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.MaxPartBytes != defaultMaxPartBytes {
		t.Errorf("MaxPartBytes = %d, want %d", cfg.MaxPartBytes, defaultMaxPartBytes)
	}
	if cfg.sendMode() {
		t.Error("sendMode = true, want false without kindle_email")
	}

	// SMTP settings alone must not enable sending without a Kindle address.
	full := validConfig()
	full["smtp_host"] = "smtp.zoho.com"
	full["smtp_port"] = 587
	full["smtp_username"] = "user@example.org"
	full["smtp_password"] = "pass"
	full["smtp_from"] = "user@example.org"
	cfg, err = loadConfig(writeConfigFile(t, full))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.sendMode() {
		t.Error("sendMode = true, want false when kindle_email is empty")
	}
}

func TestLoadConfigPartLimitBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		bytes   int
		wantErr bool
	}{
		{"floor accepted", 100_000, false},
		{"below floor rejected", 99_999, true},
		{"default accepted", 4_500_000, false},
		{"ceiling accepted", 50_000_000, false},
		{"above ceiling rejected", 50_000_001, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := validConfig()
			raw["max_part_bytes"] = tt.bytes
			_, err := loadConfig(writeConfigFile(t, raw))
			if tt.wantErr && err == nil {
				t.Fatal("loadConfig succeeded, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
		})
	}
}

func TestLoadConfigSendMode(t *testing.T) {
	full := validConfig()
	full["kindle_email"] = "reader@kindle.com"
	full["smtp_host"] = "smtp.zoho.com"
	full["smtp_port"] = 587
	full["smtp_username"] = "user@example.org"
	full["smtp_password"] = "pass"
	full["smtp_from"] = "user@example.org"

	cfg, err := loadConfig(writeConfigFile(t, full))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !cfg.sendMode() {
		t.Error("sendMode = false, want true for complete SMTP + kindle email")
	}
}

func TestLoadConfigValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"missing url", func(c map[string]any) { delete(c, "miniflux_url") }, "miniflux_url is required"},
		{"bad url scheme", func(c map[string]any) { c["miniflux_url"] = "ftp://example.org" }, "must be http or https"},
		{"missing key", func(c map[string]any) { delete(c, "miniflux_api_key") }, "miniflux_api_key is required"},
		{"missing output dir", func(c map[string]any) { delete(c, "output_dir") }, "output_dir is required"},
		{"part limit too large", func(c map[string]any) { c["max_part_bytes"] = 50_000_001 }, "max_part_bytes must be between"},
		{"part limit too small", func(c map[string]any) { c["max_part_bytes"] = 10 }, "max_part_bytes must be between"},
		{"incomplete smtp", func(c map[string]any) {
			c["kindle_email"] = "reader@kindle.com"
			c["smtp_host"] = "smtp.zoho.com"
			c["smtp_port"] = 587
			c["smtp_username"] = "user@example.org"
			c["smtp_from"] = "user@example.org"
		}, "missing smtp_password"},
		{"smtp port missing", func(c map[string]any) {
			c["kindle_email"] = "reader@kindle.com"
			c["smtp_host"] = "smtp.zoho.com"
			c["smtp_username"] = "user@example.org"
			c["smtp_password"] = "pass"
			c["smtp_from"] = "user@example.org"
		}, "missing smtp_port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := validConfig()
			tt.mutate(raw)
			_, err := loadConfig(writeConfigFile(t, raw))
			if err == nil {
				t.Fatal("loadConfig succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfigNtfyValidation(t *testing.T) {
	full := map[string]string{
		"ntfy_url":   "https://ntfy.example.org",
		"ntfy_topic": "kindle-digest",
		"ntfy_token": "tok",
	}
	tests := []struct {
		name     string
		fields   []string
		click    bool
		wantErr  string
		notifyOn bool
	}{
		{"none", nil, false, "", false},
		{"all", []string{"ntfy_url", "ntfy_topic", "ntfy_token"}, false, "", true},
		{"click only", nil, true, "", false},
		{"all plus click", []string{"ntfy_url", "ntfy_topic", "ntfy_token"}, true, "", true},
		{"url only", []string{"ntfy_url"}, false, "missing ntfy_topic, ntfy_token", false},
		{"topic only", []string{"ntfy_topic"}, false, "missing ntfy_url, ntfy_token", false},
		{"token only", []string{"ntfy_token"}, false, "missing ntfy_url, ntfy_topic", false},
		{"url and topic", []string{"ntfy_url", "ntfy_topic"}, false, "missing ntfy_token", false},
		{"url and token", []string{"ntfy_url", "ntfy_token"}, false, "missing ntfy_topic", false},
		{"topic and token", []string{"ntfy_topic", "ntfy_token"}, false, "missing ntfy_url", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := validConfig()
			for _, field := range tt.fields {
				raw[field] = full[field]
			}
			if tt.click {
				raw["ntfy_click"] = "https://example.org/digest/"
			}
			cfg, err := loadConfig(writeConfigFile(t, raw))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("loadConfig succeeded, want an error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.notifyEnabled() != tt.notifyOn {
				t.Errorf("notifyEnabled = %v, want %v", cfg.notifyEnabled(), tt.notifyOn)
			}
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := loadConfig(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("loadConfig succeeded for a missing file")
	}
}
