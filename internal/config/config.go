package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ControllerConfig holds the configuration for the CPU shrink controller.
type ControllerConfig struct {
	DefaultShrinkMode   string        `yaml:"defaultShrinkMode"`
	DefaultStartupDelay time.Duration `yaml:"defaultStartupDelay"`
	AllowedNamespaces   []string      `yaml:"allowedNamespaces"`
	ExcludedNamespaces  []string      `yaml:"excludedNamespaces"`
	ExcludedPods        []string      `yaml:"excludedPods"`
	ExcludedContainers  []string      `yaml:"excludedContainers"`
}

// DefaultConfig returns a ControllerConfig with safe defaults.
func DefaultConfig() *ControllerConfig {
	return &ControllerConfig{
		DefaultShrinkMode:   "started",
		DefaultStartupDelay: 0,
		AllowedNamespaces:   []string{},
		ExcludedNamespaces:  []string{},
		ExcludedPods:        []string{},
		ExcludedContainers:  []string{},
	}
}

// LoadConfig reads and parses a YAML configuration file.
// If the file does not exist, it returns DefaultConfig without error.
func LoadConfig(path string) (*ControllerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// Validate checks the configuration for logical errors.
func (c *ControllerConfig) Validate() error {
	if len(c.AllowedNamespaces) > 0 && len(c.ExcludedNamespaces) > 0 {
		return fmt.Errorf("allowedNamespaces and excludedNamespaces are mutually exclusive; set only one")
	}
	switch c.DefaultShrinkMode {
	case "started", "ready":
		// valid
	default:
		return fmt.Errorf("defaultShrinkMode must be %q or %q, got %q", "started", "ready", c.DefaultShrinkMode)
	}
	return nil
}
