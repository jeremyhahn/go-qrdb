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
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// client.Dimensions — 0 before first Embed, non-zero after
// ---------------------------------------------------------------------------

func TestClientDimensions_ZeroBeforeEmbed(t *testing.T) {
	_, baseURL := embeddingServer(t, func(_ http.ResponseWriter, _ *http.Request) {})
	c := newTestClient(t, baseURL)
	if c.Dimensions() != 0 {
		t.Errorf("expected 0 dims before first Embed, got %d", c.Dimensions())
	}
}

func TestClientDimensions_NonZeroAfterEmbed(t *testing.T) {
	_, baseURL := embeddingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeEmbeddingResponse("m", 768, 1))
	})
	c := newTestClient(t, baseURL)
	if _, err := c.Embed(context.Background(), []string{"hello"}, "m"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if c.Dimensions() != 768 {
		t.Errorf("expected 768 dims after Embed, got %d", c.Dimensions())
	}
}

// ---------------------------------------------------------------------------
// client.Name
// ---------------------------------------------------------------------------

func TestClientName_ReturnsConfiguredName(t *testing.T) {
	_, baseURL := embeddingServer(t, func(_ http.ResponseWriter, _ *http.Request) {})
	cfg := DefaultProviderConfig()
	cfg.BaseURL = baseURL
	cfg.Name = "my-provider"
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Name() != "my-provider" {
		t.Errorf("expected Name=my-provider, got %q", c.Name())
	}
}

func TestClientName_EmptyWhenNotSet(t *testing.T) {
	_, baseURL := embeddingServer(t, func(_ http.ResponseWriter, _ *http.Request) {})
	c := newTestClient(t, baseURL)
	if c.Name() != "" {
		t.Errorf("expected empty Name when not configured, got %q", c.Name())
	}
}

// ---------------------------------------------------------------------------
// types.DefaultConfig
// ---------------------------------------------------------------------------

func TestDefaultConfig_Provider(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Provider != "voyage" {
		t.Errorf("expected Provider=voyage, got %q", cfg.Provider)
	}
}

func TestDefaultConfig_Fields(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Model == "" {
		t.Error("expected non-empty default Model")
	}
	if cfg.Dimensions <= 0 {
		t.Errorf("expected positive Dimensions, got %d", cfg.Dimensions)
	}
	if cfg.MaxBatchSize <= 0 {
		t.Errorf("expected positive MaxBatchSize, got %d", cfg.MaxBatchSize)
	}
	if !cfg.CacheEnabled {
		t.Error("expected CacheEnabled=true in default config")
	}
	if cfg.CacheSize <= 0 {
		t.Errorf("expected positive CacheSize, got %d", cfg.CacheSize)
	}
	if cfg.MaxRetries <= 0 {
		t.Errorf("expected positive MaxRetries, got %d", cfg.MaxRetries)
	}
}

// ---------------------------------------------------------------------------
// errors.ModelNotFoundError
// ---------------------------------------------------------------------------

func TestModelNotFoundError_Error_WithoutCause(t *testing.T) {
	e := &ModelNotFoundError{Model: "voyage-unknown"}
	msg := e.Error()
	if !strings.Contains(msg, "voyage-unknown") {
		t.Errorf("expected model name in message, got %q", msg)
	}
	// Without a cause the message must not contain a nil literal.
	if strings.Contains(msg, "<nil>") {
		t.Errorf("unexpected <nil> in message: %q", msg)
	}
}

func TestModelNotFoundError_Error_WithCause(t *testing.T) {
	cause := errors.New("network timeout")
	e := &ModelNotFoundError{Model: "voyage-3", Cause: cause}
	msg := e.Error()
	if !strings.Contains(msg, "voyage-3") {
		t.Errorf("expected model name in message, got %q", msg)
	}
	if !strings.Contains(msg, "network timeout") {
		t.Errorf("expected cause in message, got %q", msg)
	}
}

func TestModelNotFoundError_Unwrap(t *testing.T) {
	cause := errors.New("root cause")
	e := &ModelNotFoundError{Model: "m", Cause: cause}
	if !errors.Is(e, cause) {
		t.Error("errors.Is should find cause via Unwrap")
	}
}

func TestModelNotFoundError_Unwrap_NilCause(t *testing.T) {
	e := &ModelNotFoundError{Model: "m"}
	if e.Unwrap() != nil {
		t.Error("expected nil Unwrap when Cause is not set")
	}
}

func TestModelNotFoundError_As(t *testing.T) {
	var target *ModelNotFoundError
	err := &ModelNotFoundError{Model: "test-model"}
	if !errors.As(err, &target) {
		t.Error("errors.As should match *ModelNotFoundError")
	}
	if target.Model != "test-model" {
		t.Errorf("expected Model=test-model, got %q", target.Model)
	}
}
