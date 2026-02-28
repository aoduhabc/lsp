package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type LSPConfig struct {
	Disabled bool     `json:"disabled"`
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	Options  any      `json:"options"`
}

type Config struct {
	DebugLSP bool                 `json:"debugLsp"`
	LSP      map[string]LSPConfig `json:"lsp"`
}

var (
	current = Config{
		LSP: map[string]LSPConfig{},
	}
	mu         sync.RWMutex
	workingDir string
)

func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	cfg := current
	return &cfg
}

func Set(cfg Config) {
	mu.Lock()
	current = cfg
	if current.LSP == nil {
		current.LSP = map[string]LSPConfig{}
	}
	mu.Unlock()
}

func WorkingDirectory() string {
	mu.RLock()
	defer mu.RUnlock()
	if workingDir != "" {
		return workingDir
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func SetWorkingDirectory(dir string) {
	mu.Lock()
	workingDir = dir
	mu.Unlock()
}

func LoadFromFile(path string) (Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.LSP == nil {
		cfg.LSP = map[string]LSPConfig{}
	}
	return cfg, nil
}
