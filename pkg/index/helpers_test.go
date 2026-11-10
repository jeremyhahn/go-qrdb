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

package index

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
)

// failingIndex is an Index that returns errors for Close and Clear operations,
// used to test error aggregation paths in the manager.
type failingIndex struct {
	closeErr error
	clearErr error
}

func (f *failingIndex) Put(_ []byte, _ []byte) error                  { return nil }
func (f *failingIndex) Get(_ []byte) ([][]byte, error)                { return nil, nil }
func (f *failingIndex) Delete(_ []byte, _ []byte) error               { return nil }
func (f *failingIndex) Scan(_, _ []byte) (map[string][][]byte, error) { return nil, nil }
func (f *failingIndex) GetAll() ([][]byte, error)                     { return nil, nil }
func (f *failingIndex) Clear() error                                  { return f.clearErr }
func (f *failingIndex) Close() error                                  { return f.closeErr }

// newTestLogger creates a logger configured for testing.
func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)
	return log
}

// uint64ToBytes encodes a uint64 as a big-endian byte slice for use as entity keys in tests.
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// setupManagerWithMemoryIndexes creates an IndexManager with ExtendedMemoryIndex
// instances pre-registered for the fields of TestEntity. This avoids the
// mockStorage iterator limitations that StoragePebbleIndex would expose.
func setupManagerWithMemoryIndexes(t *testing.T) (*IndexManager, func()) {
	t.Helper()

	log := newTestLogger(t)
	store := newMockStorage()

	mgr := &IndexManager{
		storage:  store,
		logger:   log.WithField("component", "index-manager-test"),
		indexes:  make(map[string]Index),
		metadata: make(map[string]*IndexMetadata),
	}

	// Register memory-backed indexes for TestEntity fields
	fields := map[string]*IndexMetadata{
		"ID": {
			FieldName: "ID",
			FieldType: "uint64",
			Config: IndexConfig{
				Name:          "ID",
				Type:          IndexTypeUnique,
				CaseSensitive: true,
			},
			Tag: "unique",
		},
		"Name": {
			FieldName: "Name",
			FieldType: "string",
			Config: IndexConfig{
				Name:          "Name",
				Type:          IndexTypeNormal,
				CaseSensitive: true,
			},
			Tag: "true",
		},
		"Email": {
			FieldName: "Email",
			FieldType: "string",
			Config: IndexConfig{
				Name:          "Email",
				Type:          IndexTypeUnique,
				CaseSensitive: false,
			},
			Tag: "unique,case_insensitive",
		},
	}

	for name, meta := range fields {
		idx, err := NewExtendedMemoryIndex(&meta.Config)
		require.NoError(t, err)
		mgr.indexes[name] = idx
		mgr.metadata[name] = meta
	}

	cleanup := func() {
		_ = mgr.Close()
	}

	return mgr, cleanup
}

// containsKey checks whether a [][]byte slice contains a specific key.
func containsKey(keys [][]byte, target []byte) bool {
	for _, k := range keys {
		if bytes.Equal(k, target) {
			return true
		}
	}
	return false
}
