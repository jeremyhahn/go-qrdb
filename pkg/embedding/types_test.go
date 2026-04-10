//go:build qrdb_vector

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

package embedding

import (
	"testing"
	"time"
)

func TestDefaultProviderConfig_Defaults(t *testing.T) {
	cfg := DefaultProviderConfig()
	if cfg == nil {
		t.Fatal("DefaultProviderConfig returned nil")
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("expected Timeout=30s, got %v", cfg.Timeout)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", cfg.MaxRetries)
	}
}

func TestDefaultProviderConfig_EmptyStrings(t *testing.T) {
	cfg := DefaultProviderConfig()
	if cfg.Name != "" {
		t.Errorf("expected empty Name, got %q", cfg.Name)
	}
	if cfg.BaseURL != "" {
		t.Errorf("expected empty BaseURL, got %q", cfg.BaseURL)
	}
	if cfg.APIKey != "" {
		t.Errorf("expected empty APIKey, got %q", cfg.APIKey)
	}
	if cfg.Model != "" {
		t.Errorf("expected empty Model, got %q", cfg.Model)
	}
}
