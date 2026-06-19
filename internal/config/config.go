package config

import (
	"encoding/json"
	"os"
	"sync"
)

type Config struct {
	Port          string      `json:"port"`
	APIKey        string      `json:"api_key"` // 网关自身的认证 Key
	DefaultModel  string      `json:"default_model"`
	Accounts      []Account   `json:"accounts"`
	AdminPassword string      `json:"admin_password"`
	PasswordSet   bool        `json:"password_set"`
	Pool          PoolConfig  `json:"pool,omitempty"`
}

// PoolConfig 账号池配置
type PoolConfig struct {
	MaxConcurrent int    `json:"max_concurrent,omitempty"`
	CooldownTime  int    `json:"cooldown_time,omitempty"`   // 秒
	RateLimit     int    `json:"rate_limit,omitempty"`      // 每分钟
	DailyLimit    int    `json:"daily_limit,omitempty"`     // 每天
	JitterMax     int    `json:"jitter_max,omitempty"`      // 毫秒
}

// Account 网页端账号配置
type Account struct {
	ID               string `json:"id"`
	ServiceToken     string `json:"service_token"`
	UserID           string `json:"user_id"`
	Ph               string `json:"ph"`
	Active           bool   `json:"active"`
	Source           string `json:"source,omitempty"`
	AddedAt          int64  `json:"added_at,omitempty"`
	LastValidatedAt  int64  `json:"last_validated_at,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
}

var (
	cfg  *Config
	mu   sync.RWMutex
	path string
)

func Load(p string) (*Config, error) {
	path = p
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg = &Config{Port: "7860", APIKey: "sk-mimo", DefaultModel: "mimo-v2.5-pro"}
			return cfg, Save()
		}
		return nil, err
	}
	cfg = &Config{}
	return cfg, json.Unmarshal(data, cfg)
}

func Get() Config {
	mu.RLock()
	defer mu.RUnlock()
	return *cfg
}

func Save() error {
	mu.RLock()
	data, err := json.MarshalIndent(cfg, "", "  ")
	mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func Update(fn func(*Config)) {
	mu.Lock()
	fn(cfg)
	mu.Unlock()
}
