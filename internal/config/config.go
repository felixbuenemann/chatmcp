package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/adrg/xdg"
)

type Config struct {
	DBPath       string
	AgentName    string
	PollInterval time.Duration
	LogPath      string
}

func Load() (*Config, error) {
	cfg := &Config{
		AgentName:    os.Getenv("CHATMCP_AGENT_NAME"),
		LogPath:      os.Getenv("CHATMCP_LOG_PATH"),
		PollInterval: 200 * time.Millisecond,
	}

	if v := os.Getenv("CHATMCP_POLL_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 1 {
			return nil, fmt.Errorf("CHATMCP_POLL_MS: invalid value %q", v)
		}
		cfg.PollInterval = time.Duration(ms) * time.Millisecond
	}

	if dbPath := os.Getenv("CHATMCP_DB_PATH"); dbPath != "" {
		cfg.DBPath = dbPath
	} else {
		p, err := xdg.DataFile(filepath.Join("chatmcp", "chat.db"))
		if err != nil {
			return nil, fmt.Errorf("resolving XDG data path: %w", err)
		}
		cfg.DBPath = p
	}

	return cfg, nil
}
