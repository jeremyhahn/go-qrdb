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

const (
	// BackendMemory selects the in-memory storage backend.
	BackendMemory = "memory"

	// BackendPebble selects the PebbleDB storage backend.
	BackendPebble = "pebble"

	// BackendFile selects the file-backed storage backend.
	BackendFile = "file"

	// DefaultPartitionCount is the default number of partitions.
	DefaultPartitionCount uint32 = 16

	// DefaultBackendType is the default storage backend.
	DefaultBackendType = BackendMemory
)

// Config holds partition manager configuration.
type Config struct {
	// PartitionCount is the number of partitions to create.
	PartitionCount uint32 `yaml:"partition_count"`

	// DataDir is the base directory for partition data files.
	// Required for pebble and file backends, ignored for memory.
	DataDir string `yaml:"data_dir"`

	// BackendType selects the storage backend: "memory", "pebble", or "file".
	BackendType string `yaml:"backend_type"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		PartitionCount: DefaultPartitionCount,
		BackendType:    DefaultBackendType,
	}
}

// Validate checks the configuration for invalid values.
func (c *Config) Validate() error {
	if c.PartitionCount == 0 {
		return &ConfigError{Field: "PartitionCount", Message: "must be greater than zero"}
	}

	switch c.BackendType {
	case BackendMemory, BackendPebble, BackendFile:
		// valid
	case "":
		return &ConfigError{Field: "BackendType", Message: "is required"}
	default:
		return &ConfigError{Field: "BackendType", Message: "unsupported type: " + c.BackendType}
	}

	if c.BackendType != BackendMemory && c.DataDir == "" {
		return &ConfigError{Field: "DataDir", Message: "is required for " + c.BackendType + " backend"}
	}

	return nil
}
