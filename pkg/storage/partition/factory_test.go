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
	"testing"
)

func TestMemoryFactory_Create(t *testing.T) {
	f := NewMemoryFactory()
	cfg := &Config{PartitionCount: 4, BackendType: BackendMemory}

	s, err := f.Create(0, cfg)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Verify the storage works.
	if err := s.Set([]byte("key"), []byte("value")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	val, err := s.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value" {
		t.Errorf("Get = %q, want %q", val, "value")
	}
}

func TestMemoryFactory_BackendType(t *testing.T) {
	f := NewMemoryFactory()
	if got := f.BackendType(); got != BackendMemory {
		t.Errorf("BackendType() = %q, want %q", got, BackendMemory)
	}
}

func TestPebbleFactory_BackendType(t *testing.T) {
	f := NewPebbleFactory()
	if got := f.BackendType(); got != BackendPebble {
		t.Errorf("BackendType() = %q, want %q", got, BackendPebble)
	}
}

func TestFileFactory_BackendType(t *testing.T) {
	f := NewFileFactory()
	if got := f.BackendType(); got != BackendFile {
		t.Errorf("BackendType() = %q, want %q", got, BackendFile)
	}
}

func TestFileFactory_Create(t *testing.T) {
	f := NewFileFactory()
	cfg := &Config{PartitionCount: 2, BackendType: BackendFile, DataDir: t.TempDir()}

	s, err := f.Create(0, cfg)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("key"), []byte("value")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
}

func TestPebbleFactory_Create(t *testing.T) {
	f := NewPebbleFactory()
	cfg := &Config{PartitionCount: 2, BackendType: BackendPebble, DataDir: t.TempDir()}

	s, err := f.Create(0, cfg)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Set([]byte("key"), []byte("value")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
}

func TestPebbleFactory_Create_InvalidDir(t *testing.T) {
	f := NewPebbleFactory()
	// Use a path that cannot be created (nested under a non-existent file).
	cfg := &Config{PartitionCount: 2, BackendType: BackendPebble, DataDir: "/dev/null/invalid/path"}

	_, err := f.Create(0, cfg)
	if err == nil {
		t.Fatal("expected error for invalid directory")
	}
}
