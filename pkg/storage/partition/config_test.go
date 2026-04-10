// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package partition

import (
	"errors"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.PartitionCount != DefaultPartitionCount {
		t.Errorf("PartitionCount = %d, want %d", cfg.PartitionCount, DefaultPartitionCount)
	}
	if cfg.BackendType != DefaultBackendType {
		t.Errorf("BackendType = %q, want %q", cfg.BackendType, DefaultBackendType)
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name:   "valid memory config",
			config: &Config{PartitionCount: 4, BackendType: BackendMemory},
		},
		{
			name:   "valid pebble config",
			config: &Config{PartitionCount: 4, BackendType: BackendPebble, DataDir: "/tmp/data"},
		},
		{
			name:   "valid file config",
			config: &Config{PartitionCount: 4, BackendType: BackendFile, DataDir: "/tmp/data"},
		},
		{
			name:    "zero partition count",
			config:  &Config{PartitionCount: 0, BackendType: BackendMemory},
			wantErr: "PartitionCount",
		},
		{
			name:    "empty backend type",
			config:  &Config{PartitionCount: 4, BackendType: ""},
			wantErr: "BackendType",
		},
		{
			name:    "unsupported backend type",
			config:  &Config{PartitionCount: 4, BackendType: "redis"},
			wantErr: "unsupported type: redis",
		},
		{
			name:    "pebble without data dir",
			config:  &Config{PartitionCount: 4, BackendType: BackendPebble},
			wantErr: "DataDir",
		},
		{
			name:    "file without data dir",
			config:  &Config{PartitionCount: 4, BackendType: BackendFile},
			wantErr: "DataDir",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var cfgErr *ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("expected ConfigError, got %T: %v", err, err)
			}
			if cfgErr.Field != tt.wantErr && cfgErr.Message != tt.wantErr {
				// Check if the error message contains the expected substring.
				if cfgErr.Field != tt.wantErr {
					errStr := err.Error()
					found := false
					if len(tt.wantErr) > 0 {
						for i := 0; i <= len(errStr)-len(tt.wantErr); i++ {
							if errStr[i:i+len(tt.wantErr)] == tt.wantErr {
								found = true
								break
							}
						}
					}
					if !found {
						t.Errorf("error %q does not contain %q", errStr, tt.wantErr)
					}
				}
			}
		})
	}
}
