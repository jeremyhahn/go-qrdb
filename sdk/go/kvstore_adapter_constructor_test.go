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

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestNewKVStoreAdapter verifies that a valid construction returns a working
// KVStore that delegates to the supplied transport clients.
func TestNewKVStoreAdapter(t *testing.T) {
	var capturedKey string
	kv := &mockTransportKV{
		putFn: func(_ context.Context, key string, _ []byte, _ ...transport.WriteOption) error {
			capturedKey = key
			return nil
		},
	}

	store, err := NewKVStoreAdapter(kv, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	if store == nil {
		t.Fatal("NewKVStoreAdapter returned nil store")
	}

	if err := store.Put(context.Background(), "k1", []byte("v1")); err != nil {
		t.Fatalf("Put through constructed adapter: %v", err)
	}
	if capturedKey != "k1" {
		t.Errorf("adapter delegated key %q, want %q", capturedKey, "k1")
	}
}

// TestNewKVStoreAdapterNilInputs verifies typed errors for nil dependencies.
func TestNewKVStoreAdapterNilInputs(t *testing.T) {
	t.Run("nil kv client", func(t *testing.T) {
		store, err := NewKVStoreAdapter(nil, &mockTransportIndex{})
		if store != nil {
			t.Error("expected nil store")
		}
		var nilKV *NilKVClientError
		if !errors.As(err, &nilKV) {
			t.Fatalf("expected *NilKVClientError, got %T: %v", err, err)
		}
		if err.Error() == "" {
			t.Error("error message is empty")
		}
	})

	t.Run("nil index client", func(t *testing.T) {
		store, err := NewKVStoreAdapter(&mockTransportKV{}, nil)
		if store != nil {
			t.Error("expected nil store")
		}
		var nilIdx *NilIndexClientError
		if !errors.As(err, &nilIdx) {
			t.Fatalf("expected *NilIndexClientError, got %T: %v", err, err)
		}
		if err.Error() == "" {
			t.Error("error message is empty")
		}
	})
}
