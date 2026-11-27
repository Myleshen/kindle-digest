package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	defaultMaxPartBytes = 4_500_000
	hardMaxPartBytes    = 5_000_000
	minMaxPartBytes     = 100_000
)

// Config is the JSON configuration file.
type Config struct {
	MinifluxURL    string `json:"miniflux_url"`
	MinifluxAPIKey string `json:"miniflux_api_key"`
	KindleEmail    string `json:"kindle_email"`
	SMTPHost       string `json:"smtp_host"`
	SMTPPort       int    `json:"smtp_port"`
	SMTPUsername   string `json:"smtp_username"`
	SMTPPassword   string `json:"smtp_password"`
	SMTPFrom       string `json:"smtp_from"`
	OutputDir      string `json:"output_dir"`
	LogFile        string `json:"log_file"`
	MaxPartBytes   int    `json:"max_part_bytes"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if cfg.MaxPartBytes == 0 {
		cfg.MaxPartBytes = defaultMaxPartBytes
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.MinifluxURL == "" {
		return fmt.Errorf("miniflux_url is required")
	}
	u, err := url.Parse(c.MinifluxURL)
	if err != nil {
		return fmt.Errorf("miniflux_url %q is not a valid URL: %w", c.MinifluxURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("miniflux_url must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("miniflux_url %q has no host", c.MinifluxURL)
	}
	if c.MinifluxAPIKey == "" {
		return fmt.Errorf("miniflux_api_key is required")
	}
	if c.OutputDir == "" {
		return fmt.Errorf("output_dir is required")
	}
	if c.MaxPartBytes < minMaxPartBytes || c.MaxPartBytes > hardMaxPartBytes {
		return fmt.Errorf("max_part_bytes must be between %d and %d, got %d", minMaxPartBytes, hardMaxPartBytes, c.MaxPartBytes)
	}

	if c.KindleEmail != "" {
		var missing []string
		for _, f := range []struct {
			name  string
			value string
		}{
			{"smtp_host", c.SMTPHost},
			{"smtp_username", c.SMTPUsername},
			{"smtp_password", c.SMTPPassword},
			{"smtp_from", c.SMTPFrom},
		} {
			if strings.TrimSpace(f.value) == "" {
				missing = append(missing, f.name)
			}
		}
		if c.SMTPPort == 0 {
			missing = append(missing, "smtp_port")
		}
		if len(missing) > 0 {
			return fmt.Errorf("kindle_email is set but SMTP settings are incomplete: missing %s", strings.Join(missing, ", "))
		}
		if c.SMTPPort < 1 || c.SMTPPort > 65535 {
			return fmt.Errorf("smtp_port must be between 1 and 65535, got %d", c.SMTPPort)
		}
	}
	return nil
}

// sendMode reports whether the digest should be emailed or only generated.
func (c *Config) sendMode() bool {
	return c.KindleEmail != "" &&
		c.SMTPHost != "" && c.SMTPPort != 0 &&
		c.SMTPUsername != "" && c.SMTPPassword != "" && c.SMTPFrom != ""
}
