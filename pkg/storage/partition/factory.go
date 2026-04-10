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
	"path/filepath"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// Factory creates storage instances for individual partitions.
type Factory interface {
	// Create creates a new storage instance for the given partition.
	Create(partitionID uint32, config *Config) (storage.Storage, error)

	// BackendType returns the name of the storage backend.
	BackendType() string
}

// MemoryFactory creates in-memory storage instances.
type MemoryFactory struct{}

// NewMemoryFactory creates a new MemoryFactory.
func NewMemoryFactory() *MemoryFactory {
	return &MemoryFactory{}
}

// Create creates a new in-memory storage instance.
func (f *MemoryFactory) Create(_ uint32, _ *Config) (storage.Storage, error) {
	return memory.NewStorage(), nil
}

// BackendType returns "memory".
func (f *MemoryFactory) BackendType() string {
	return BackendMemory
}

// PebbleFactory creates PebbleDB storage instances.
type PebbleFactory struct{}

// NewPebbleFactory creates a new PebbleFactory.
func NewPebbleFactory() *PebbleFactory {
	return &PebbleFactory{}
}

// Create creates a new PebbleDB storage instance for the given partition.
// Each partition gets its own subdirectory under config.DataDir.
func (f *PebbleFactory) Create(partitionID uint32, config *Config) (storage.Storage, error) {
	dir := filepath.Join(config.DataDir, "partition-"+strconv.FormatUint(uint64(partitionID), 10))
	pebbleCfg := pebblestore.DefaultConfig(dir)
	s, err := pebblestore.New(pebbleCfg)
	if err != nil {
		return nil, &PartitionCreateError{PartitionID: partitionID, Err: err}
	}
	return s, nil
}

// BackendType returns "pebble".
func (f *PebbleFactory) BackendType() string {
	return BackendPebble
}

// FileFactory creates file-backed storage instances.
type FileFactory struct{}

// NewFileFactory creates a new FileFactory.
func NewFileFactory() *FileFactory {
	return &FileFactory{}
}

// Create creates a new file-backed storage instance for the given partition.
// Each partition gets its own subdirectory under config.DataDir.
func (f *FileFactory) Create(partitionID uint32, config *Config) (storage.Storage, error) {
	dir := filepath.Join(config.DataDir, "partition-"+strconv.FormatUint(uint64(partitionID), 10))
	s, err := file.New(dir)
	if err != nil {
		return nil, &PartitionCreateError{PartitionID: partitionID, Err: err}
	}
	return s, nil
}

// BackendType returns "file".
func (f *FileFactory) BackendType() string {
	return BackendFile
}
