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

package kvstore

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryKVStore_PutIfAllAbsent_CreatesWhenAllAbsent(t *testing.T) {
	s := NewMemoryKVStore()
	created, err := s.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
	if err != nil || !created {
		t.Fatalf("PutIfAllAbsent = %v, %v; want true, nil", created, err)
	}
	got, _ := s.Get(context.Background(), "meta/acme")
	if string(got) != "v" {
		t.Fatalf("Get = %q; want v", got)
	}
}

func TestMemoryKVStore_PutIfAllAbsent_RefusedByAFence(t *testing.T) {
	s := NewMemoryKVStore()
	ctx := context.Background()
	if err := s.Put(ctx, "tomb/acme", []byte("retired")); err != nil {
		t.Fatal(err)
	}
	created, err := s.PutIfAllAbsent(ctx, "meta/acme", []byte("v"), "other", "tomb/acme")
	if err != nil || created {
		t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
	}
	if got, _ := s.Get(ctx, "meta/acme"); got != nil {
		t.Fatal("a refused fenced create wrote the key")
	}
}

func TestMemoryKVStore_PutIfAllAbsent_RefusedByThePresentKey(t *testing.T) {
	s := NewMemoryKVStore()
	ctx := context.Background()
	if err := s.Put(ctx, "meta/acme", []byte("winner")); err != nil {
		t.Fatal(err)
	}
	created, err := s.PutIfAllAbsent(ctx, "meta/acme", []byte("loser"))
	if err != nil || created {
		t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
	}
	if got, _ := s.Get(ctx, "meta/acme"); string(got) != "winner" {
		t.Fatalf("value = %q; the refused create overwrote it", got)
	}
}

func TestMemoryKVStore_PutIfAllAbsent_HonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewMemoryKVStore().PutIfAllAbsent(ctx, "k", []byte("v")); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutIfAllAbsent on a cancelled context = %v; want context.Canceled", err)
	}
}
