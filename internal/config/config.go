package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the agent configuration
type Config struct {
	MoneatURL    string
	AgentKey     string
	PollInterval time.Duration
	IngestPath   string
}

func LoadFromEnv() *Config {
	pollInterval := 60 * time.Second
	if val := os.Getenv("POLL_INTERVAL"); val != "" {
		if seconds, err := strconv.Atoi(val); err == nil {
			pollInterval = time.Duration(seconds) * time.Second
		}
	}

	return &Config{
		MoneatURL:    getEnv("MONEAT_URL", "https://api.moneat.dev"),
		AgentKey:     getEnv("MONEAT_KEY", ""),
		PollInterval: pollInterval,
		IngestPath:   "/api/v1/monitor/ingest",
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
