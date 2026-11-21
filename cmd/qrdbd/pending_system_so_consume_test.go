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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// consumeTestNotFoundError is the not-found shape firstboot's record readers
// recognise (a KeyNotFound method).
type consumeTestNotFoundError struct{ key string }

func (e *consumeTestNotFoundError) Error() string       { return "not found: " + e.key }
func (e *consumeTestNotFoundError) KeyNotFound() string { return e.key }

// consumeTestStore is a map-backed firstboot.PendingAdminCertStore whose
// Put can be made to fail.
type consumeTestStore struct {
	records map[string][]byte
	puts    int
	putErr  error
}

func newConsumeTestStore() *consumeTestStore {
	return &consumeTestStore{records: map[string][]byte{}}
}

func (s *consumeTestStore) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if _, ok := s.records[key]; ok {
		return false, nil
	}
	s.records[key] = value
	return true, nil
}

func (s *consumeTestStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := s.records[key]
	if !ok {
		return nil, &consumeTestNotFoundError{key: key}
	}
	return v, nil
}

func (s *consumeTestStore) Put(_ context.Context, key string, value []byte) error {
	s.puts++
	if s.putErr != nil {
		return s.putErr
	}
	s.records[key] = value
	return nil
}

func (s *consumeTestStore) Delete(_ context.Context, key string) error {
	delete(s.records, key)
	return nil
}

// TestConsumePendingSystemSOAfterFirstBoot (PR #1579 review round 2, P1):
// the node whose first boot ran with the pending system SO record consumes
// it before serving, so a later deletion of the SO cannot leave the record
// pending for the next start to re-create the account from; a failed
// consumption fails the boot.
func TestConsumePendingSystemSOAfterFirstBoot(t *testing.T) {
	ctx := context.Background()

	t.Run("no pending SO writes nothing", func(t *testing.T) {
		store := newConsumeTestStore()
		if err := consumePendingSystemSOAfterFirstBoot(ctx, store, nil); err != nil {
			t.Fatalf("consume with no pending SO = %v, want nil", err)
		}
		if store.puts != 0 || len(store.records) != 0 {
			t.Fatalf("consume with no pending SO wrote %d records (%d puts)", len(store.records), store.puts)
		}
	})

	t.Run("a pending record becomes consumed", func(t *testing.T) {
		store := newConsumeTestStore()
		store.records[firstboot.PendingSystemSOKey] = []byte(`{"username":"secofficer"}`)
		if err := consumePendingSystemSOAfterFirstBoot(ctx, store, &firstboot.SOConfig{Username: "secofficer"}); err != nil {
			t.Fatalf("consume = %v, want nil", err)
		}
		if !firstboot.SystemSOConsumed(ctx, store) {
			t.Fatal("the pending system SO record is still pending after first boot ran with it")
		}
	})

	t.Run("a failed write fails the boot", func(t *testing.T) {
		store := newConsumeTestStore()
		store.records[firstboot.PendingSystemSOKey] = []byte(`{"username":"secofficer"}`)
		store.putErr = errors.New("store unavailable")
		err := consumePendingSystemSOAfterFirstBoot(ctx, store, &firstboot.SOConfig{Username: "secofficer"})
		var bootErr *AuthBootstrapError
		if !errors.As(err, &bootErr) {
			t.Fatalf("consume with a failing store = %v (%T), want *AuthBootstrapError", err, err)
		}
		if firstboot.SystemSOConsumed(ctx, store) {
			t.Fatal("a failed write reports the record consumed")
		}
	})
}
