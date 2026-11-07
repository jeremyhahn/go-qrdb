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

import (
	"errors"
	"testing"
)

// validateVolumePools is reached through Config.Validate; every rejection
// names the offending pool in ValidationError.Field.

// TestValidateVolumePools_Valid verifies that a well-formed pool list is
// accepted.
func TestValidateVolumePools_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "alpha", Path: "/data/a", Provider: "dir"},
		{Name: "beta", Path: "/data/b", Provider: "luks2", Options: map[string]string{"device": "/dev/nvme1n1"}},
		{Name: "gamma", Path: "/data/c"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid pool list, got %v", err)
	}
}

// TestValidateVolumePools_EmptyName verifies that a pool with an empty name
// is rejected.
func TestValidateVolumePools_EmptyName(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "", Path: "/data/x"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty pool name, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[].name" {
		t.Errorf("Field: got %q, want storage.volume_pools[].name", ve.Field)
	}
}

// TestValidateVolumePools_DuplicateName verifies that two pools with the
// same name are rejected.
func TestValidateVolumePools_DuplicateName(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "shared", Path: "/data/1"},
		{Name: "shared", Path: "/data/2"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate pool name, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[shared]" {
		t.Errorf("Field: got %q, want storage.volume_pools[shared]", ve.Field)
	}
}

// TestValidateVolumePools_EmptyPath verifies that a pool with an empty path
// is rejected.
func TestValidateVolumePools_EmptyPath(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "empty", Path: ""},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for pool with empty path, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[empty].path" {
		t.Errorf("Field: got %q, want storage.volume_pools[empty].path", ve.Field)
	}
}

// TestValidateVolumePools_RelativePath verifies that a pool path that is
// not absolute is rejected.
func TestValidateVolumePools_RelativePath(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "bad", Path: "relative/path"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for relative path, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[bad].path" {
		t.Errorf("Field: got %q, want storage.volume_pools[bad].path", ve.Field)
	}
}

// TestValidateVolumePools_OverlappingPaths verifies that two pools whose
// paths overlap are rejected.
func TestValidateVolumePools_OverlappingPaths(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "fast", Path: "/mnt/vol"},
		{Name: "bulk", Path: "/mnt/vol/sub"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for overlapping pool paths, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[fast].path" {
		t.Errorf("Field: got %q, want storage.volume_pools[fast].path", ve.Field)
	}
}

// TestValidateVolumePools_InsideDataDir verifies that a pool path inside
// the node's Raft data directory is rejected.
func TestValidateVolumePools_InsideDataDir(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "nested", Path: cfg.Raft.DataDir + "/pool"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for pool path inside data dir, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[nested].path" {
		t.Errorf("Field: got %q, want storage.volume_pools[nested].path", ve.Field)
	}
}

// TestValidateVolumePools_ContainsDataDir verifies that a pool path that
// contains the node's Raft data directory is rejected.
func TestValidateVolumePools_ContainsDataDir(t *testing.T) {
	cfg := validConfig()
	cfg.Storage.VolumePools = []VolumePoolConfig{
		{Name: "parent", Path: "/tmp"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for pool path containing data dir, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "storage.volume_pools[parent].path" {
		t.Errorf("Field: got %q, want storage.volume_pools[parent].path", ve.Field)
	}
}
