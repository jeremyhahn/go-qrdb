// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package database

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// A Shamir M-of-N is held to 2 <= M <= N <= MaxShamirShares, the bounds the
// seal splits a key under; a barrier that is disabled or not Shamir is not
// checked here.
func TestDatabaseBarrierConfig_ValidateShamirScheme(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   *DatabaseBarrierConfig
		valid bool
	}{
		{"nil", nil, true},
		{"disabled", &DatabaseBarrierConfig{Mode: BarrierModeShamir, SharesTotal: 999}, true},
		{"passphrase", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase}, true},
		{"2-of-2", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 2}, true},
		{"2-of-255", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 255}, true},
		{"255-of-255", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 255, SharesTotal: 255}, true},
		{"2-of-256", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 256}, false},
		{"1-of-3", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 1, SharesTotal: 3}, false},
		{"4-of-3", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 4, SharesTotal: 3}, false},
		{"0-of-0", &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			var scheme *InvalidShamirSchemeError
			if tc.valid != (err == nil) || (!tc.valid && !errors.As(err, &scheme)) {
				t.Fatalf("Validate = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}

// A create with a Shamir M-of-N the seal cannot split is refused before the
// manager writes anything: no database directory and no catalog entry.
func TestManager_CreateRefusesAnUnsplittableShamirScheme(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), dir)
	t.Cleanup(func() { _ = m.Close() })
	_, err := m.CreateWithCredentials(context.Background(), DatabaseConfig{
		Name: "vault", Engine: EngineKV, Backend: BackendMemory, DataDir: t.TempDir(),
		Barrier: &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 256},
	}, seal.Credentials{})
	var scheme *InvalidShamirSchemeError
	if !errors.As(err, &scheme) || scheme.SharesTotal != 256 {
		t.Fatalf("create = %v (%T), want InvalidShamirSchemeError", err, err)
	}
	if !strings.Contains(err.Error(), "255") {
		t.Fatalf("the refusal names the bound: %q", err.Error())
	}
	if _, err := m.Get("vault"); err == nil {
		t.Fatal("the refused create left a database behind")
	}
	if _, err := os.Stat(filepath.Join(dir, "databases", "vault")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused create wrote to disk: %v", err)
	}
}
