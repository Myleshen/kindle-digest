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
	hardMaxPartBytes    = 50_000_000
	minMaxPartBytes     = 100_000

	defaultIMAPHost       = "imap.zoho.in"
	defaultIMAPPort       = 993
	defaultMaxEmailsTotal = 5
	defaultMaxPerSource   = 1
)

// EmailSource is one newsletter sender read read-only over IMAP. FromName is
// optional and matches the From display name; TLDR's editions share one
// address and can only be told apart that way.
type EmailSource struct {
	Name         string `json:"name"`
	Folder       string `json:"folder"`
	From         string `json:"from"`
	FromName     string `json:"from_name"`
	SubjectMatch string `json:"subject_match"`
	MaxPerDay    int    `json:"max_per_day"`
}

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
	WebToken       string `json:"web_token"`

	IMAPHost       string        `json:"imap_host"`
	IMAPPort       int           `json:"imap_port"`
	IMAPUsername   string        `json:"imap_username"`
	IMAPPassword   string        `json:"imap_password"`
	MaxEmailsTotal int           `json:"max_emails_total"`
	EmailSources   []EmailSource `json:"email_sources"`
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
	if cfg.IMAPHost == "" {
		cfg.IMAPHost = defaultIMAPHost
	}
	if cfg.IMAPPort == 0 {
		cfg.IMAPPort = defaultIMAPPort
	}
	if cfg.MaxEmailsTotal == 0 {
		cfg.MaxEmailsTotal = defaultMaxEmailsTotal
	}
	for i := range cfg.EmailSources {
		if cfg.EmailSources[i].MaxPerDay == 0 {
			cfg.EmailSources[i].MaxPerDay = defaultMaxPerSource
		}
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

	if len(c.EmailSources) > 0 {
		var missing []string
		if strings.TrimSpace(c.IMAPUsername) == "" {
			missing = append(missing, "imap_username")
		}
		if strings.TrimSpace(c.IMAPPassword) == "" {
			missing = append(missing, "imap_password")
		}
		if strings.TrimSpace(c.IMAPHost) == "" {
			missing = append(missing, "imap_host")
		}
		if c.IMAPPort < 1 || c.IMAPPort > 65535 {
			return fmt.Errorf("imap_port must be between 1 and 65535, got %d", c.IMAPPort)
		}
		if len(missing) > 0 {
			return fmt.Errorf("email_sources is set but IMAP settings are incomplete: missing %s", strings.Join(missing, ", "))
		}
		if c.MaxEmailsTotal < 1 {
			return fmt.Errorf("max_emails_total must be at least 1, got %d", c.MaxEmailsTotal)
		}
		for i, src := range c.EmailSources {
			var fields []string
			if strings.TrimSpace(src.Name) == "" {
				fields = append(fields, "name")
			}
			if strings.TrimSpace(src.Folder) == "" {
				fields = append(fields, "folder")
			}
			if strings.TrimSpace(src.From) == "" {
				fields = append(fields, "from")
			}
			if len(fields) > 0 {
				return fmt.Errorf("email_sources[%d] is missing %s", i, strings.Join(fields, ", "))
			}
			if src.MaxPerDay < 0 {
				return fmt.Errorf("email_sources[%d] max_per_day must not be negative, got %d", i, src.MaxPerDay)
			}
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
