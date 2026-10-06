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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestNewVolumeCatalogKVServicer_GetTranslatesNotFoundToNilNil proves the
// one real behavior this adapter exists for: a service.KeyNotFoundError (or
// anything wrapping one) becomes (nil, nil), matching volume.KVServicer's
// contract, so Catalog's "no Assignment/Pool for this id" reads see an
// absence rather than a hard failure.
func TestNewVolumeCatalogKVServicer_GetTranslatesNotFoundToNilNil(t *testing.T) {
	inner := &mockKVServicer{
		getFn: func(context.Context, string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "missing"}
		},
	}
	kv := NewVolumeCatalogKVServicer(inner)

	v, err := kv.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get(missing) error = %v, want nil", err)
	}
	if v != nil {
		t.Fatalf("Get(missing) value = %v, want nil", v)
	}
}

// TestNewVolumeCatalogKVServicer_GetPassesThroughOtherErrors proves ONLY a
// not-found is translated; any other error (a real storage failure) must
// still reach the caller as an error, not be swallowed into (nil, nil).
func TestNewVolumeCatalogKVServicer_GetPassesThroughOtherErrors(t *testing.T) {
	wantErr := errors.New("storage unavailable")
	inner := &mockKVServicer{
		getFn: func(context.Context, string) ([]byte, error) {
			return nil, wantErr
		},
	}
	kv := NewVolumeCatalogKVServicer(inner)

	_, err := kv.Get(context.Background(), "key")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Get error = %v, want %v", err, wantErr)
	}
}

// TestNewVolumeCatalogKVServicer_GetSuccessPassesThroughTheValue proves the
// ordinary success path returns the underlying value unchanged.
func TestNewVolumeCatalogKVServicer_GetSuccessPassesThroughTheValue(t *testing.T) {
	want := []byte("payload")
	inner := &mockKVServicer{
		getFn: func(context.Context, string) ([]byte, error) {
			return want, nil
		},
	}
	kv := NewVolumeCatalogKVServicer(inner)

	got, err := kv.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Get value = %q, want %q", got, want)
	}
}

// TestNewVolumeCatalogKVServicer_ScanForwardsToTheUnderlyingServicer proves
// Scan reaches the wrapped service.KVServicer's own Scan (every KVServicer
// this codebase constructs implements the scanner assertion).
func TestNewVolumeCatalogKVServicer_ScanForwardsToTheUnderlyingServicer(t *testing.T) {
	want := map[string][]byte{"a": []byte("1")}
	inner := &mockKVServicer{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			if prefix != "p/" {
				t.Fatalf("Scan prefix = %q, want p/", prefix)
			}
			return want, nil
		},
	}
	kv := NewVolumeCatalogKVServicer(inner)

	got, err := kv.Scan(context.Background(), "p/")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || string(got["a"]) != "1" {
		t.Fatalf("Scan result = %v, want %v", got, want)
	}
}

// TestNewVolumeCatalogKVServicer_PutIsPromotedUnchanged proves the embedded
// service.KVServicer's other methods (Put, PutIfAbsent, CompareAndSwap,
// CompareAndDelete, Delete, List, ListAll, ...) are promoted straight
// through, not reimplemented -- spot-checked here with Put.
func TestNewVolumeCatalogKVServicer_PutIsPromotedUnchanged(t *testing.T) {
	var gotKey string
	var gotValue []byte
	inner := &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			gotKey, gotValue = key, value
			return nil
		},
	}
	kv := NewVolumeCatalogKVServicer(inner)

	if err := kv.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if gotKey != "k" || string(gotValue) != "v" {
		t.Fatalf("Put forwarded (%q, %q), want (k, v)", gotKey, gotValue)
	}
}
