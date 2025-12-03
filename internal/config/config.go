package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/fusionn-muse/pkg/logger"
)

// Config holds all configuration for the application.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Whisper   WhisperConfig   `mapstructure:"whisper"`
	Translate TranslateConfig `mapstructure:"translate"`
	Apprise   AppriseConfig   `mapstructure:"apprise"`
	Queue     QueueConfig     `mapstructure:"queue"`
}

type ServerConfig struct {
	Port int `mapstructure:"port"`
}

// Folders returns hardcoded folder paths (user mounts via Docker volumes).
func Folders() FoldersConfig {
	return FoldersConfig{
		Input:     "/data/input",      // Mount: torrent download folder
		Staging:   "/data/staging",    // Internal: queue before processing
		Process:   "/data/processing", // Internal: active processing
		Finished:  "/data/finished",   // Mount: completed videos
		Subtitles: "/data/subtitles",  // Mount: translated subtitles
		Failed:    "/data/failed",     // Failed jobs (for manual inspection)
	}
}

type FoldersConfig struct {
	Input     string // Source files from torrent client
	Staging   string // Queue before processing
	Process   string // Active processing
	Finished  string // Completed videos
	Subtitles string // Translated subtitles
	Failed    string // Failed jobs
}

type WhisperConfig struct {
	// Provider: "local" (whisper.cpp) or "openai" (API)
	Provider string `mapstructure:"provider"`
	// Model: for local = "base", "small", "medium", "large"
	//        for openai = "whisper-1"
	Model string `mapstructure:"model"`
	// APIKey: required if provider is "openai"
	APIKey string `mapstructure:"api_key"`
	// Language: source language hint (optional, "auto" for auto-detect)
	Language string `mapstructure:"language"`
}

type TranslateConfig struct {
	// Provider: "openai", "claude", "gemini", "openrouter", "custom"
	Provider string `mapstructure:"provider"`
	// Model: e.g., "gpt-4o-mini", "claude-3-haiku"
	Model string `mapstructure:"model"`
	// APIKey: API key for the provider
	APIKey string `mapstructure:"api_key"`
	// TargetLang: e.g., "Simplified Chinese", "Japanese"
	TargetLang string `mapstructure:"target_lang"`

	// Custom endpoint settings (for provider: "custom")
	CustomServer   string `mapstructure:"custom_server"`   // e.g., "http://localhost:1234"
	CustomEndpoint string `mapstructure:"custom_endpoint"` // e.g., "/v1/chat/completions"

	// Rate limiting
	RateLimitRPM int `mapstructure:"rate_limit_rpm"` // Requests per minute (0 = no limit)

	// Additional CLI args for llm-subtrans
	Args []string `mapstructure:"args"`
}

type AppriseConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	BaseURL string `mapstructure:"base_url"` // Apprise API URL
	Key     string `mapstructure:"key"`      // Apprise config key
	Tag     string `mapstructure:"tag"`      // Tag to filter services
}

type QueueConfig struct {
	MaxRetries   int `mapstructure:"max_retries"`   // Max retries per job
	RetryDelayMs int `mapstructure:"retry_delay_ms"` // Delay between retries
}

// ChangeCallback is called when config changes.
type ChangeCallback func(old, new *Config)

// Manager handles config loading and hot-reload.
type Manager struct {
	mu        sync.RWMutex
	cfg       *Config
	callbacks []ChangeCallback
	stop      chan struct{}

	path        string
	lastModTime time.Time
}

// NewManager creates a config manager with hot-reload support via polling.
func NewManager(path string) (*Manager, error) {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	viper.SetEnvPrefix("FUSIONN_MUSE")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	var lastMod time.Time
	if stat, err := os.Stat(path); err == nil {
		lastMod = stat.ModTime()
	}

	m := &Manager{
		cfg:         &cfg,
		stop:        make(chan struct{}),
		path:        path,
		lastModTime: lastMod,
	}

	go m.pollForChanges(10 * time.Second)

	logger.Infof("📋 Config loaded (polling every 10s for changes)")

	return m, nil
}

func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

func (m *Manager) OnChange(cb ChangeCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks = append(m.callbacks, cb)
}

func (m *Manager) Stop() {
	close(m.stop)
}

func (m *Manager) pollForChanges(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			stat, err := os.Stat(m.path)
			if err != nil {
				continue
			}

			m.mu.RLock()
			lastMod := m.lastModTime
			m.mu.RUnlock()

			if stat.ModTime().After(lastMod) {
				logger.Infof("🔄 Config file changed, reloading...")

				if err := viper.ReadInConfig(); err != nil {
					logger.Errorf("❌ Failed to re-read config: %v", err)
					continue
				}

				m.mu.Lock()
				m.lastModTime = stat.ModTime()
				m.mu.Unlock()

				m.reload()
			}
		}
	}
}

func (m *Manager) reload() {
	var newCfg Config
	if err := viper.Unmarshal(&newCfg); err != nil {
		logger.Errorf("❌ Failed to reload config: %v", err)
		return
	}

	m.mu.Lock()
	oldCfg := m.cfg
	m.cfg = &newCfg
	callbacks := m.callbacks
	m.mu.Unlock()

	logChanges(oldCfg, &newCfg, "")

	for _, cb := range callbacks {
		cb(oldCfg, &newCfg)
	}
}

func logChanges(old, cur any, prefix string) {
	oldVal := reflect.ValueOf(old)
	newVal := reflect.ValueOf(cur)

	if oldVal.Kind() == reflect.Ptr {
		oldVal = oldVal.Elem()
	}
	if newVal.Kind() == reflect.Ptr {
		newVal = newVal.Elem()
	}

	if oldVal.Kind() != reflect.Struct {
		return
	}

	t := oldVal.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		oldField := oldVal.Field(i)
		newField := newVal.Field(i)

		fieldName := field.Name
		if prefix != "" {
			fieldName = prefix + "." + fieldName
		}

		if oldField.Kind() == reflect.Struct {
			logChanges(oldField.Interface(), newField.Interface(), fieldName)
			continue
		}

		if !reflect.DeepEqual(oldField.Interface(), newField.Interface()) {
			oldStr := formatValue(oldField)
			newStr := formatValue(newField)
			logger.Infof("  📝 %s: %s → %s", fieldName, oldStr, newStr)
		}
	}
}

func formatValue(v reflect.Value) string {
	return fmt.Sprintf("%v", v.Interface())
}

// Load is a convenience function for one-time loading.
func Load(path string) (*Config, error) {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	viper.SetEnvPrefix("FUSIONN_MUSE")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
