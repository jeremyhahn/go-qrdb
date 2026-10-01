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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// fencedCreators returns the two production FencedCreator implementations
// over one evaluating transaction service, so each case runs against both.
func fencedCreators(tx *evaluatingTxService) map[string]kvstore.FencedCreator {
	return map[string]kvstore.FencedCreator{
		"kvSealBackend":        kvSealBackend{KVStore: newKVStoreAdapter(&mockKVServicer{}, nil), tx: tx},
		"fencedKVStoreAdapter": &fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: &mockKVServicer{}}, tx: tx},
	}
}

// TestPutIfAllAbsent_CreatesWhenEveryKeyIsAbsent: the transaction's compares
// hold, the put is applied, and created=true.
func TestPutIfAllAbsent_CreatesWhenEveryKeyIsAbsent(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			tx := newEvaluatingTxService()
			fc := fencedCreators(tx)[name]
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if err != nil || !created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want true, nil", created, err)
			}
			if got := tx.data["meta/acme"]; got == nil || string(got.Value) != "v" {
				t.Fatalf("the put was not applied: %+v", got)
			}
		})
	}
}

// TestPutIfAllAbsent_RefusedWhenAFenceIsPresent is the property #484 depends
// on: a present fence refuses the create inside the same transaction, and the
// store is unchanged.
func TestPutIfAllAbsent_RefusedWhenAFenceIsPresent(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			tx := newEvaluatingTxService()
			tx.put("tomb/acme", []byte("retired"))
			fc := fencedCreators(tx)[name]
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if err != nil || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
			}
			if _, ok := tx.data["meta/acme"]; ok {
				t.Fatal("a refused fenced create wrote the key")
			}
		})
	}
}

// TestPutIfAllAbsent_RefusedWhenTheKeyIsPresent: the created key itself is a
// fence too, so an existing record is never overwritten.
func TestPutIfAllAbsent_RefusedWhenTheKeyIsPresent(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			tx := newEvaluatingTxService()
			tx.put("meta/acme", []byte("winner"))
			fc := fencedCreators(tx)[name]
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("loser"), "tomb/acme")
			if err != nil || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
			}
			if string(tx.data["meta/acme"].Value) != "winner" {
				t.Fatal("a refused fenced create overwrote the existing record")
			}
		})
	}
}

// TestPutIfAllAbsent_WithoutTransactionServiceRefuses: no transaction plane,
// no capability -- the typed refusal, never a check-then-write.
func TestPutIfAllAbsent_WithoutTransactionServiceRefuses(t *testing.T) {
	for name, fc := range map[string]kvstore.FencedCreator{
		"kvSealBackend":        kvSealBackend{KVStore: newKVStoreAdapter(&mockKVServicer{}, nil)},
		"fencedKVStoreAdapter": &fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: &mockKVServicer{}}},
	} {
		t.Run(name, func(t *testing.T) {
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if !errors.Is(err, ErrNoTransactionService) || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, ErrNoTransactionService", created, err)
			}
		})
	}
}

// TestKVStoreAdapter_WithoutTransactionPlaneIsNotFenced pins the reason the
// fenced adapter is a distinct type: the plain adapter must not claim the
// capability it cannot honour.
func TestKVStoreAdapter_WithoutTransactionPlaneIsNotFenced(t *testing.T) {
	if _, ok := newKVStoreAdapter(&mockKVServicer{}, nil).(kvstore.FencedCreator); ok {
		t.Fatal("kvStoreAdapter satisfies kvstore.FencedCreator without a transaction plane")
	}
}
