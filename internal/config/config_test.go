package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.DefaultShrinkMode != "started" {
		t.Errorf("DefaultShrinkMode = %q, want %q", cfg.DefaultShrinkMode, "started")
	}
	if cfg.DefaultStartupDelay != 0 {
		t.Errorf("DefaultStartupDelay = %v, want 0", cfg.DefaultStartupDelay)
	}
	if len(cfg.AllowedNamespaces) != 0 {
		t.Errorf("AllowedNamespaces = %v, want empty", cfg.AllowedNamespaces)
	}
	if len(cfg.ExcludedNamespaces) != 0 {
		t.Errorf("ExcludedNamespaces = %v, want empty", cfg.ExcludedNamespaces)
	}
	if len(cfg.ExcludedPods) != 0 {
		t.Errorf("ExcludedPods = %v, want empty", cfg.ExcludedPods)
	}
	if len(cfg.ExcludedContainers) != 0 {
		t.Errorf("ExcludedContainers = %v, want empty", cfg.ExcludedContainers)
	}
}

func TestValidate_ValidConfigs(t *testing.T) {
	tests := []struct {
		name string
		cfg  *ControllerConfig
	}{
		{
			name: "defaults",
			cfg:  DefaultConfig(),
		},
		{
			name: "ready mode",
			cfg: &ControllerConfig{
				DefaultShrinkMode: "ready",
			},
		},
		{
			name: "only allowedNamespaces",
			cfg: &ControllerConfig{
				DefaultShrinkMode: "started",
				AllowedNamespaces: []string{"prod"},
			},
		},
		{
			name: "only excludedNamespaces",
			cfg: &ControllerConfig{
				DefaultShrinkMode:  "started",
				ExcludedNamespaces: []string{"kube-system"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestValidate_InvalidConfigs(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *ControllerConfig
		wantMsg string
	}{
		{
			name: "invalid shrink mode",
			cfg: &ControllerConfig{
				DefaultShrinkMode: "bogus",
			},
			wantMsg: "defaultShrinkMode",
		},
		{
			name: "both allowed and excluded namespaces",
			cfg: &ControllerConfig{
				DefaultShrinkMode:  "started",
				AllowedNamespaces:  []string{"prod"},
				ExcludedNamespaces: []string{"kube-system"},
			},
			wantMsg: "mutually exclusive",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatal("Validate() expected error, got nil")
			}
			if tc.wantMsg != "" {
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tc.wantMsg)
				}
			}
		})
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error for missing file: %v", err)
	}
	// Should return defaults
	if cfg.DefaultShrinkMode != "started" {
		t.Errorf("expected default shrink mode, got %q", cfg.DefaultShrinkMode)
	}
}

func TestLoadConfig_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
defaultShrinkMode: ready
defaultStartupDelay: 30s
allowedNamespaces:
  - prod
  - staging
excludedPods:
  - prod/coredns-abc123
excludedContainers:
  - vault-agent
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error: %v", err)
	}
	if cfg.DefaultShrinkMode != "ready" {
		t.Errorf("DefaultShrinkMode = %q, want %q", cfg.DefaultShrinkMode, "ready")
	}
	if cfg.DefaultStartupDelay != 30*time.Second {
		t.Errorf("DefaultStartupDelay = %v, want 30s", cfg.DefaultStartupDelay)
	}
	if len(cfg.AllowedNamespaces) != 2 || cfg.AllowedNamespaces[0] != "prod" || cfg.AllowedNamespaces[1] != "staging" {
		t.Errorf("AllowedNamespaces = %v, want [prod staging]", cfg.AllowedNamespaces)
	}
	if len(cfg.ExcludedPods) != 1 || cfg.ExcludedPods[0] != "prod/coredns-abc123" {
		t.Errorf("ExcludedPods = %v, unexpected", cfg.ExcludedPods)
	}
	if len(cfg.ExcludedContainers) != 1 || cfg.ExcludedContainers[0] != "vault-agent" {
		t.Errorf("ExcludedContainers = %v, unexpected", cfg.ExcludedContainers)
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// yaml.v3 is lenient with plain strings; use a tab character in a flow mapping to trigger a parse error.
	if err := os.WriteFile(path, []byte("defaultShrinkMode: [\t\ninvalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() expected error for invalid YAML, got nil")
	}
}

func TestLoadConfig_ValidationFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
defaultShrinkMode: invalid
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() expected validation error, got nil")
	}
}

func TestLoadConfig_MutuallyExclusiveNamespaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
defaultShrinkMode: started
allowedNamespaces:
  - prod
excludedNamespaces:
  - kube-system
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() expected error for mutually exclusive namespaces, got nil")
	}
}

func TestLoadConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() unexpected error for empty file: %v", err)
	}
	// Should return defaults since empty YAML doesn't override anything
	if cfg.DefaultShrinkMode != "started" {
		t.Errorf("expected default shrink mode, got %q", cfg.DefaultShrinkMode)
	}
}

