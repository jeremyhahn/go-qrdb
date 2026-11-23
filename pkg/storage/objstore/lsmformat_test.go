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

package objstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/memory"
)

func readMarker(t *testing.T, store interface {
	GetWithContext(context.Context, string) (io.ReadCloser, error)
}) string {
	t.Helper()
	rc, err := store.GetWithContext(context.Background(), formatMarkerName)
	if err != nil {
		t.Fatalf("get marker: %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return string(b)
}

func TestEnsureLSMFormat_MarksEmptyStore(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	if err := EnsureLSMFormat(ctx, store); err != nil {
		t.Fatalf("EnsureLSMFormat: %v", err)
	}
	if got := readMarker(t, store); got != "3" {
		t.Fatalf("marker = %q, want 3", got)
	}
	if err := EnsureLSMFormat(ctx, store); err != nil {
		t.Fatalf("second EnsureLSMFormat: %v", err)
	}
}

func TestEnsureLSMFormat_RefusesFlatMarker(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	if err := store.PutWithContext(ctx, formatMarkerName, strings.NewReader("2")); err != nil {
		t.Fatal(err)
	}
	err := EnsureLSMFormat(ctx, store)
	var mm *FormatVersionMismatchError
	if !errors.As(err, &mm) || mm.Found != "2" || mm.Want != "3" {
		t.Fatalf("err = %v, want FormatVersionMismatchError Found 2 Want 3", err)
	}
	if !strings.Contains(err.Error(), "no migration") {
		t.Fatalf("error text %q does not say no migration", err)
	}
}

func TestEnsureLSMFormat_RefusesUnmarkedStore(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	if err := store.PutWithContext(ctx, "tenant/db1/000001.sst", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	err := EnsureLSMFormat(ctx, store)
	var missing *LSMFormatMarkerMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *LSMFormatMarkerMissingError", err)
	}
	if !strings.Contains(err.Error(), "no migration") {
		t.Fatalf("error text %q does not say no migration", err)
	}
}

func TestEnsureLSMFormat_NilStore(t *testing.T) {
	var invalid *InvalidRemoteError
	if err := EnsureLSMFormat(context.Background(), nil); !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *InvalidRemoteError", err)
	}
}

func TestEnsureLSMFormat_FlatEngineRefusesLSMStore(t *testing.T) {
	store := memory.New()
	if err := EnsureLSMFormat(context.Background(), store); err != nil {
		t.Fatalf("EnsureLSMFormat: %v", err)
	}
	err := (&Engine{store: store, provider: "memory"}).ensureFormatMarker()
	var mm *FormatVersionMismatchError
	if !errors.As(err, &mm) || mm.Found != "3" || mm.Want != "2" {
		t.Fatalf("err = %v, want FormatVersionMismatchError Found 3 Want 2", err)
	}
}

func TestEnsureLSMFormat_RemoteListsNoMarker(t *testing.T) {
	store := memory.New()
	if err := EnsureLSMFormat(context.Background(), store); err != nil {
		t.Fatalf("EnsureLSMFormat: %v", err)
	}
	r, err := NewRemote(store, "tenant/db1")
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	w, err := r.CreateObject("000001.sst")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	names, err := r.List("", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, n := range names {
		if strings.Contains(n, formatMarkerName) {
			t.Fatalf("Remote.List returned the marker: %v", names)
		}
	}
	if len(names) != 1 {
		t.Fatalf("Remote.List = %v, want the one object", names)
	}
}
