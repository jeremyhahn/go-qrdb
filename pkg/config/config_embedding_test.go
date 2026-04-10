// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEmbeddingConfig_Defaults(t *testing.T) {
	cfg := &EmbeddingConfig{}
	if cfg.DefaultProvider != "" {
		t.Errorf("expected empty DefaultProvider, got %q", cfg.DefaultProvider)
	}
	if cfg.CacheCapacity != 0 {
		t.Errorf("expected CacheCapacity=0, got %d", cfg.CacheCapacity)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected empty Providers, got %d entries", len(cfg.Providers))
	}
}

func TestEmbeddingConfig_YAMLRoundTrip(t *testing.T) {
	original := EmbeddingConfig{
		DefaultProvider: "ollama",
		CacheCapacity:   1024,
		Providers: []EmbeddingProviderConfig{
			{
				Name:       "ollama",
				BaseURL:    "http://localhost:11434/v1",
				APIKey:     "",
				Model:      "nomic-embed-text",
				Timeout:    "30s",
				MaxRetries: 3,
			},
			{
				Name:       "openai",
				BaseURL:    "https://api.openai.com/v1",
				APIKey:     "sk-secret",
				Model:      "text-embedding-3-small",
				Timeout:    "15s",
				MaxRetries: 5,
			},
		},
	}

	// Marshal to YAML.
	b, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	// Unmarshal back.
	var decoded EmbeddingConfig
	if err := yaml.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if decoded.DefaultProvider != original.DefaultProvider {
		t.Errorf("DefaultProvider: got %q, want %q", decoded.DefaultProvider, original.DefaultProvider)
	}
	if decoded.CacheCapacity != original.CacheCapacity {
		t.Errorf("CacheCapacity: got %d, want %d", decoded.CacheCapacity, original.CacheCapacity)
	}
	if len(decoded.Providers) != len(original.Providers) {
		t.Fatalf("Providers: got %d, want %d", len(decoded.Providers), len(original.Providers))
	}
	for i, p := range original.Providers {
		got := decoded.Providers[i]
		if got.Name != p.Name {
			t.Errorf("Providers[%d].Name: got %q, want %q", i, got.Name, p.Name)
		}
		if got.BaseURL != p.BaseURL {
			t.Errorf("Providers[%d].BaseURL: got %q, want %q", i, got.BaseURL, p.BaseURL)
		}
		if got.APIKey != p.APIKey {
			t.Errorf("Providers[%d].APIKey: got %q, want %q", i, got.APIKey, p.APIKey)
		}
		if got.Model != p.Model {
			t.Errorf("Providers[%d].Model: got %q, want %q", i, got.Model, p.Model)
		}
		if got.Timeout != p.Timeout {
			t.Errorf("Providers[%d].Timeout: got %q, want %q", i, got.Timeout, p.Timeout)
		}
		if got.MaxRetries != p.MaxRetries {
			t.Errorf("Providers[%d].MaxRetries: got %d, want %d", i, got.MaxRetries, p.MaxRetries)
		}
	}
}

func TestEmbeddingConfig_ParsedFromFullConfig(t *testing.T) {
	content := `
server:
  admin_port: 8080
  grpc_port: 9090
  host: "127.0.0.1"
raft:
  data_dir: "/tmp/qrdb-test"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
  sm_type: "memory"
storage:
  backend: "memory"
tls:
  enabled: false
logging:
  level: "info"
  format: "json"
discovery:
  method: "static"
embedding:
  default_provider: "ollama"
  cache_capacity: 512
  providers:
    - name: "ollama"
      base_url: "http://localhost:11434/v1"
      model: "nomic-embed-text"
      timeout: "30s"
      max_retries: 3
`
	p := writeTemp(t, content)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Embedding.DefaultProvider != "ollama" {
		t.Errorf("DefaultProvider: got %q, want %q", cfg.Embedding.DefaultProvider, "ollama")
	}
	if cfg.Embedding.CacheCapacity != 512 {
		t.Errorf("CacheCapacity: got %d, want 512", cfg.Embedding.CacheCapacity)
	}
	if len(cfg.Embedding.Providers) != 1 {
		t.Fatalf("Providers: got %d, want 1", len(cfg.Embedding.Providers))
	}
	p0 := cfg.Embedding.Providers[0]
	if p0.Name != "ollama" {
		t.Errorf("Providers[0].Name: got %q, want %q", p0.Name, "ollama")
	}
	if p0.Model != "nomic-embed-text" {
		t.Errorf("Providers[0].Model: got %q, want %q", p0.Model, "nomic-embed-text")
	}
	if p0.MaxRetries != 3 {
		t.Errorf("Providers[0].MaxRetries: got %d, want 3", p0.MaxRetries)
	}
}

func TestEmbeddingProviderConfig_Defaults(t *testing.T) {
	p := &EmbeddingProviderConfig{}
	if p.Name != "" {
		t.Errorf("expected empty Name, got %q", p.Name)
	}
	if p.BaseURL != "" {
		t.Errorf("expected empty BaseURL, got %q", p.BaseURL)
	}
	if p.MaxRetries != 0 {
		t.Errorf("expected MaxRetries=0, got %d", p.MaxRetries)
	}
}
