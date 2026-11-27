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

package vector

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// failedSweepStorage seeds namespace "ns" with one vector and deletes it
// while every vector delete fails, so the sweep stops part-way.
func failedSweepStorage(t *testing.T) (*KVVectorStorage, *faultKV) {
	t.Helper()
	s, f := seededFaultStorage(t)
	f.op, f.prefix = "Delete", kvVectorPrefix
	if err := s.DeleteNamespace(context.Background(), "ns"); !errors.Is(err, errFault) {
		t.Fatalf("DeleteNamespace with failing sweep: got %v, want errFault", err)
	}
	if got := keysWithPrefix(t, f, kvRetiredPrefix); len(got) != 1 {
		t.Fatalf("pending sweep keys: got %v, want one", got)
	}
	if got := keysWithPrefix(t, f, kvVectorPrefix); len(got) != 1 {
		t.Fatalf("unswept vectors: got %v, want one", got)
	}
	f.op = ""
	return s, f
}

func keysWithPrefix(t *testing.T, f *faultKV, prefix string) []string {
	t.Helper()
	keys, err := f.GuardedMemoryStore.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// TestKVVectorStorage_DeleteNamespaceRetryFinishesSweep: a retried delete
// of a name whose sweep failed finishes it and succeeds; once nothing is
// pending the name is not found.
func TestKVVectorStorage_DeleteNamespaceRetryFinishesSweep(t *testing.T) {
	s, f := failedSweepStorage(t)
	ctx := context.Background()
	if err := s.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatalf("retry DeleteNamespace: %v", err)
	}
	if got := keysWithPrefix(t, f, ""); len(got) != 0 {
		t.Fatalf("keys left after the retry: %v", got)
	}
	if err := s.DeleteNamespace(ctx, "ns"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Fatalf("DeleteNamespace with nothing pending: got %v, want ErrNamespaceNotFound", err)
	}
}

// TestKVVectorStorage_ReopenDrainsDeletedNamespace: after a restart,
// CleanupTombstones or Compact finishes the interrupted sweep and leaves
// no key behind.
func TestKVVectorStorage_ReopenDrainsDeletedNamespace(t *testing.T) {
	for name, drain := range map[string]func(*KVVectorStorage) error{
		"CleanupTombstones": func(s *KVVectorStorage) error { return s.CleanupTombstones(context.Background(), 0) },
		"Compact":           func(s *KVVectorStorage) error { return s.Compact(context.Background(), "") },
	} {
		t.Run(name, func(t *testing.T) {
			_, f := failedSweepStorage(t)
			reopened := newKVStorage(t, f)
			if err := drain(reopened); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := keysWithPrefix(t, f, ""); len(got) != 0 {
				t.Fatalf("keys left after %s: %v", name, got)
			}
		})
	}
}

// TestKVVectorStorage_RecreatedNamespaceKeepsItsVectors: finishing the
// retired incarnation's sweep never touches a recreated namespace's
// vectors.
func TestKVVectorStorage_RecreatedNamespaceKeepsItsVectors(t *testing.T) {
	s, f := failedSweepStorage(t)
	ctx := context.Background()
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns"}); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if err := s.UpsertVector(ctx, "ns", kvVec(2, nil)); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := s.Compact(ctx, "ns"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := keysWithPrefix(t, f, kvRetiredPrefix); len(got) != 0 {
		t.Fatalf("pending sweep keys after Compact: %v", got)
	}
	if got := keysWithPrefix(t, f, kvVectorPrefix); len(got) != 1 {
		t.Fatalf("vectors after Compact: got %v, want only the recreated one", got)
	}
	if v, err := s.GetVector(ctx, "ns", 2); err != nil || v == nil {
		t.Fatalf("recreated vector: %v, %v", v, err)
	}
	if v, err := s.GetVector(ctx, "ns", 1); err == nil && v != nil {
		t.Fatalf("retired vector visible: %v", v)
	}
}
