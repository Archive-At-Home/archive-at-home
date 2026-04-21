package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the node configuration
type Config struct {
	Node struct {
		ID        string `yaml:"id"`        // Unique node identifier
		Signature string `yaml:"signature"` // ED25519 signature (Base64 encoded)
	} `yaml:"node"`

	Server struct {
		URL string `yaml:"url"` // WebSocket server URL (e.g., ws://localhost:8080/ws)
	} `yaml:"server"`

	EHentai struct {
		Cookie      string `yaml:"cookie"`       // EHentai cookie (ipb_member_id=xxx; ipb_pass_hash=xxx)
		UseExhentai bool   `yaml:"use_exhentai"` // Whether to use ExHentai instead of E-Hentai
		MaxGPCost   int    `yaml:"max_gp_cost"`  // Maximum GP cost per day (-1 for unlimited)
	} `yaml:"ehentai"`

	Task struct {
		BaseBalanceGP  int `yaml:"base_balance_gp"`  // Base balance for delay calculation (default: 5000)
		BaseClaimDelay int `yaml:"base_claim_delay"` // Base claim delay in seconds for low balance nodes (default: 5)
	} `yaml:"task"`

	Database struct {
		Path string `yaml:"path"` // SQLite database path (default: ./data/ehentai.db)
	} `yaml:"database"`

	Dashboard struct {
		Enabled bool   `yaml:"enabled"` // Whether to enable the dashboard (default: false)
		Address string `yaml:"address"` // Dashboard server address (default: :8090)
	} `yaml:"dashboard"`
}

// Load reads the configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Validate required fields
	if cfg.Node.ID == "" {
		return nil, fmt.Errorf("node.id is required")
	}
	if cfg.Node.Signature == "" {
		return nil, fmt.Errorf("node.signature is required")
	}
	if cfg.Server.URL == "" {
		return nil, fmt.Errorf("server.url is required")
	}
	if cfg.EHentai.Cookie == "" {
		return nil, fmt.Errorf("ehentai.cookie is required")
	}
	if cfg.Database.Path == "" {
		return nil, fmt.Errorf("database.path is required")
	}
	if cfg.Task.BaseBalanceGP == 0 {
		return nil, fmt.Errorf("task.base_balance_gp is required")
	}
	if cfg.Task.BaseClaimDelay == 0 {
		return nil, fmt.Errorf("task.base_claim_delay is required")
	}

	return &cfg, nil
}
