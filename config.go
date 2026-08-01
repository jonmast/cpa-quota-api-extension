package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCacheTTL       = 30 * time.Minute
	defaultRequestTimeout = 30 * time.Second
	defaultMaxConcurrency = 8
	defaultDatabasePath   = "./data/cpa-quota-api-extension.db"
)

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		CacheTTL:        defaultCacheTTL,
		RequestTimeout:  defaultRequestTimeout,
		MaxConcurrency:  defaultMaxConcurrency,
		IncludeDisabled: false,
		DatabasePath:    defaultDatabasePath, HealthRefreshInterval: time.Minute, HistoryInterval: 5 * time.Minute,
		FailureWindow: 10 * time.Minute, DegradedThreshold: 3, IncidentRetention: 7 * 24 * time.Hour,
		IncidentMaxRows: 10000, HistoryRetention: 30 * 24 * time.Hour, HistoryMaxRows: 10000,
		HealthQueue: 1024, WebhookTimeout: 10 * time.Second, LostThreshold: 1, DegradedPoolThreshold: 1,
		AlertCooldown: 15 * time.Minute,
	}
}

func decodeLifecycleConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	var req lifecycleRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return cfg, fmt.Errorf("decode lifecycle request: %w", err)
	}
	text, err := lifecycleConfigText(req.ConfigYAML)
	if err != nil {
		return cfg, err
	}
	values := yamlScalars(text)
	if value := values["cache-ttl"]; value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Minute {
			return cfg, fmt.Errorf("cache-ttl must be a Go duration >= 1m")
		}
		cfg.CacheTTL = parsed
	}
	if value := values["request-timeout"]; value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second || parsed > 5*time.Minute {
			return cfg, fmt.Errorf("request-timeout must be between 1s and 5m")
		}
		cfg.RequestTimeout = parsed
	}
	if value := values["max-concurrency"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 64 {
			return cfg, fmt.Errorf("max-concurrency must be between 1 and 64")
		}
		cfg.MaxConcurrency = parsed
	}
	if value := values["include-disabled"]; value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, fmt.Errorf("include-disabled must be boolean")
		}
		cfg.IncludeDisabled = parsed
	}
	if value := values["database-path"]; value != "" {
		cfg.DatabasePath = value
	}
	if err := applyHealthDuration(values, "health-refresh-interval", &cfg.HealthRefreshInterval, 10*time.Second, 24*time.Hour); err != nil {
		return cfg, err
	}
	if err := applyHealthDuration(values, "health-history-interval", &cfg.HistoryInterval, 10*time.Second, 24*time.Hour); err != nil {
		return cfg, err
	}
	if err := applyHealthDuration(values, "failure-window", &cfg.FailureWindow, time.Second, 24*time.Hour); err != nil {
		return cfg, err
	}
	if err := applyHealthDuration(values, "incident-retention", &cfg.IncidentRetention, time.Hour, 365*24*time.Hour); err != nil {
		return cfg, err
	}
	if err := applyHealthDuration(values, "history-retention", &cfg.HistoryRetention, time.Hour, 365*24*time.Hour); err != nil {
		return cfg, err
	}
	if err := applyHealthDuration(values, "webhook-timeout", &cfg.WebhookTimeout, time.Second, 5*time.Minute); err != nil {
		return cfg, err
	}
	if err := applyHealthDuration(values, "alert-cooldown", &cfg.AlertCooldown, time.Second, 24*time.Hour); err != nil {
		return cfg, err
	}
	if value := values["webhook-url"]; value != "" {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return cfg, fmt.Errorf("webhook-url must be an http or https URL")
		}
		cfg.WebhookURL = value
	}
	for key, target := range map[string]*int{"degraded-failure-threshold": &cfg.DegradedThreshold, "incident-max-rows": &cfg.IncidentMaxRows, "history-max-rows": &cfg.HistoryMaxRows, "usage-queue-size": &cfg.HealthQueue, "alert-lost-threshold": &cfg.LostThreshold, "alert-degraded-threshold": &cfg.DegradedPoolThreshold} {
		if value := values[key]; value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 0 {
				return cfg, fmt.Errorf("%s must be a non-negative integer", key)
			}
			if (key == "incident-max-rows" || key == "history-max-rows") && (parsed < 100 || parsed > 1000000) {
				return cfg, fmt.Errorf("%s must be between 100 and 1000000", key)
			}
			if key == "usage-queue-size" && (parsed < 64 || parsed > 65536) {
				return cfg, fmt.Errorf("usage-queue-size must be between 64 and 65536")
			}
			*target = parsed
		}
	}
	return cfg, nil
}

func applyHealthDuration(values map[string]string, key string, target *time.Duration, min, max time.Duration) error {
	if value := values[key]; value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < min || parsed > max {
			return fmt.Errorf("%s must be between %s and %s", key, min, max)
		}
		*target = parsed
	}
	return nil
}

func lifecycleConfigText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if decoded, decodeErr := base64.StdEncoding.DecodeString(text); decodeErr == nil && strings.Contains(string(decoded), ":") {
			return string(decoded), nil
		}
		return text, nil
	}
	var bytes []byte
	if err := json.Unmarshal(raw, &bytes); err == nil {
		return string(bytes), nil
	}
	return "", fmt.Errorf("config_yaml must be a string or byte array")
}

func yamlScalars(raw string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(strings.SplitN(parts[1], "#", 2)[0])
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values
}
