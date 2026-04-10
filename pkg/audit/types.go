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

// Package audit provides configurable dual-backend audit logging with
// support for internal KVStore database and/or filesystem JSON log output.
package audit

import "time"

const (
	// ResultSuccess indicates the audited action completed successfully.
	ResultSuccess = "success"

	// ResultDenied indicates the audited action was denied.
	ResultDenied = "denied"
)

// Entry represents a single audit log event.
type Entry struct {
	Timestamp time.Time         `json:"timestamp"`
	TenantID  string            `json:"tenantID"`
	Subject   string            `json:"subject"`
	Action    string            `json:"action"`
	Resource  string            `json:"resource"`
	Result    string            `json:"result"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Config configures the audit logging system.
type Config struct {
	Enabled     bool   `yaml:"enabled"`
	KVBackend   bool   `yaml:"kv_backend"`
	FileBackend bool   `yaml:"file_backend"`
	FilePath    string `yaml:"file_path"`
}

// DefaultConfig returns a Config with sensible defaults: file backend
// enabled, KV backend disabled, writing to "audit.log".
func DefaultConfig() Config {
	return Config{
		Enabled:     true,
		KVBackend:   false,
		FileBackend: true,
		FilePath:    "audit.log",
	}
}
