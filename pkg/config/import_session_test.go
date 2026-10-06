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

package config

import "testing"

func TestResolvedMaxSessionBytes(t *testing.T) {
	tests := []struct {
		name string
		cfg  ImportSessionConfig
		want int64
	}{
		{"unset resolves to default", ImportSessionConfig{}, DefaultImportSessionMaxBytes},
		{"negative resolves to default", ImportSessionConfig{MaxSessionBytes: -1}, DefaultImportSessionMaxBytes},
		{"explicit value passes through", ImportSessionConfig{MaxSessionBytes: 1024}, 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedMaxSessionBytes(); got != tt.want {
				t.Errorf("ResolvedMaxSessionBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolvedMaxChunksPerSession(t *testing.T) {
	tests := []struct {
		name string
		cfg  ImportSessionConfig
		want int
	}{
		{"unset resolves to default", ImportSessionConfig{}, DefaultImportSessionMaxChunks},
		{"negative resolves to default", ImportSessionConfig{MaxChunksPerSession: -1}, DefaultImportSessionMaxChunks},
		{"explicit value passes through", ImportSessionConfig{MaxChunksPerSession: 7}, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedMaxChunksPerSession(); got != tt.want {
				t.Errorf("ResolvedMaxChunksPerSession() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolvedMaxSessionsPerTenant(t *testing.T) {
	tests := []struct {
		name string
		cfg  ImportSessionConfig
		want int
	}{
		{"unset resolves to default", ImportSessionConfig{}, DefaultImportSessionsPerTenant},
		{"negative resolves to default", ImportSessionConfig{MaxSessionsPerTenant: -1}, DefaultImportSessionsPerTenant},
		{"explicit value passes through", ImportSessionConfig{MaxSessionsPerTenant: 3}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedMaxSessionsPerTenant(); got != tt.want {
				t.Errorf("ResolvedMaxSessionsPerTenant() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolvedSessionTTLSeconds(t *testing.T) {
	tests := []struct {
		name string
		cfg  ImportSessionConfig
		want int64
	}{
		{"unset resolves to default", ImportSessionConfig{}, DefaultImportSessionTTLSeconds},
		{"negative resolves to default", ImportSessionConfig{SessionTTLSeconds: -1}, DefaultImportSessionTTLSeconds},
		{"explicit value passes through", ImportSessionConfig{SessionTTLSeconds: 60}, 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ResolvedSessionTTLSeconds(); got != tt.want {
				t.Errorf("ResolvedSessionTTLSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDefaultConfigImportSessionDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ImportSession.MaxSessionBytes != DefaultImportSessionMaxBytes {
		t.Errorf("MaxSessionBytes = %d, want %d", cfg.ImportSession.MaxSessionBytes, DefaultImportSessionMaxBytes)
	}
	if cfg.ImportSession.MaxChunksPerSession != DefaultImportSessionMaxChunks {
		t.Errorf("MaxChunksPerSession = %d, want %d", cfg.ImportSession.MaxChunksPerSession, DefaultImportSessionMaxChunks)
	}
	if cfg.ImportSession.MaxSessionsPerTenant != DefaultImportSessionsPerTenant {
		t.Errorf("MaxSessionsPerTenant = %d, want %d", cfg.ImportSession.MaxSessionsPerTenant, DefaultImportSessionsPerTenant)
	}
	if cfg.ImportSession.SessionTTLSeconds != DefaultImportSessionTTLSeconds {
		t.Errorf("SessionTTLSeconds = %d, want %d", cfg.ImportSession.SessionTTLSeconds, DefaultImportSessionTTLSeconds)
	}
}
