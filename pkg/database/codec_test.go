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

//go:build codec_json

package database

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestDatabaseConfig_Validate_StorageCodec verifies that a valid registered
// codec passes validation.
func TestDatabaseConfig_Validate_StorageCodec(t *testing.T) {
	cfg := DatabaseConfig{
		Name:         "codec-db",
		Engine:       EngineKV,
		Backend:      BackendMemory,
		Mode:         ModeEmbedded,
		StorageCodec: "json",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error for known codec: %v", err)
	}
}

// TestDatabaseConfig_Validate_InvalidStorageCodec verifies that an unregistered
// codec name is rejected with InvalidStorageCodecError.
func TestDatabaseConfig_Validate_InvalidStorageCodec(t *testing.T) {
	cfg := DatabaseConfig{
		Name:         "codec-db",
		Engine:       EngineKV,
		Backend:      BackendMemory,
		Mode:         ModeEmbedded,
		StorageCodec: "nonexistent_codec",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error for unregistered codec, got nil")
	}
	var codecErr *InvalidStorageCodecError
	if !errors.As(err, &codecErr) {
		t.Errorf("Validate() error type = %T, want *InvalidStorageCodecError", err)
	}
}

// TestDatabaseConfig_Validate_EmptyStorageCodec verifies that an empty
// storage codec is accepted (it means "use the global default").
func TestDatabaseConfig_Validate_EmptyStorageCodec(t *testing.T) {
	cfg := DatabaseConfig{
		Name:         "codec-db",
		Engine:       EngineKV,
		Backend:      BackendMemory,
		Mode:         ModeEmbedded,
		StorageCodec: "",
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error for empty codec: %v", err)
	}
}

// TestDatabaseMeta_StorageCodec_RoundTrip verifies that StorageCodec is
// preserved when DatabaseMeta is marshaled to and unmarshaled from JSON.
func TestDatabaseMeta_StorageCodec_RoundTrip(t *testing.T) {
	meta := DatabaseMeta{
		Name:         "meta-db",
		Engine:       EngineKV,
		Backend:      BackendMemory,
		StorageCodec: "json",
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got DatabaseMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.StorageCodec != "json" {
		t.Errorf("StorageCodec = %q, want %q", got.StorageCodec, "json")
	}
}

// TestDatabaseMeta_StorageCodec_OmittedWhenEmpty verifies that StorageCodec
// is omitted from JSON output when empty, preserving backward compatibility.
func TestDatabaseMeta_StorageCodec_OmittedWhenEmpty(t *testing.T) {
	meta := DatabaseMeta{
		Name:    "compat-db",
		Engine:  EngineKV,
		Backend: BackendMemory,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	content := string(data)
	if contains(content, "storage_codec") {
		t.Errorf("JSON should not contain storage_codec when empty; got: %s", content)
	}
}

// TestDatabaseInfo_StorageCodec_Propagated verifies that StorageCodec is
// included in DatabaseInfo serialization.
func TestDatabaseInfo_StorageCodec_Propagated(t *testing.T) {
	info := DatabaseInfo{
		Name:         "info-db",
		Engine:       EngineKV,
		Backend:      BackendMemory,
		Status:       StatusOpen,
		StorageCodec: "json",
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got DatabaseInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.StorageCodec != "json" {
		t.Errorf("StorageCodec = %q, want %q", got.StorageCodec, "json")
	}
}

// TestInvalidStorageCodecError_Message verifies the error message format.
func TestInvalidStorageCodecError_Message(t *testing.T) {
	err := &InvalidStorageCodecError{Codec: "badcodec"}
	want := `database: invalid or unsupported storage codec: "badcodec"`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// contains is a substring check helper used in this test file.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
