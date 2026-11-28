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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// keyguardKV records the keys of the writes that reach it.
type keyguardKV struct {
	service.KVServicer
	writes []string
}

func (k *keyguardKV) Put(_ context.Context, key string, _ []byte) error {
	k.writes = append(k.writes, key)
	return nil
}

func (k *keyguardKV) PutWithMode(_ context.Context, key string, _ []byte, _ writemode.Mode) error {
	k.writes = append(k.writes, key)
	return nil
}

func (k *keyguardKV) PutIfAbsent(_ context.Context, key string, _ []byte) (bool, error) {
	k.writes = append(k.writes, key)
	return true, nil
}

func (k *keyguardKV) CompareAndSwap(_ context.Context, key string, _, _ []byte) (bool, error) {
	k.writes = append(k.writes, key)
	return true, nil
}

func (k *keyguardKV) Delete(_ context.Context, key string) error {
	k.writes = append(k.writes, key)
	return nil
}

func (k *keyguardKV) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	k.writes = append(k.writes, key)
	return nil
}

func (k *keyguardKV) CompareAndDelete(_ context.Context, key string, _ []byte) (bool, error) {
	k.writes = append(k.writes, key)
	return true, nil
}

// keyguardBatch counts the batches that reach it.
type keyguardBatch struct {
	service.BatchServicer
	batches int
}

func (b *keyguardBatch) Execute(context.Context, []service.Op) (*service.BatchResponse, error) {
	b.batches++
	return &service.BatchResponse{}, nil
}

func (b *keyguardBatch) ExecuteWithMode(context.Context, []service.Op, writemode.Mode) (*service.BatchResponse, error) {
	b.batches++
	return &service.BatchResponse{}, nil
}

func TestSQLiteKeyGuard_KVWritesIntoADatabaseAreRefused(t *testing.T) {
	kv := &keyguardKV{}
	svcs := withSQLiteKeyGuard(&DatabaseServices{KV: kv, Batch: &keyguardBatch{}})
	ctx := context.Background()
	for _, key := range []string{"sqlite/app/meta", "sqlite/app/page/0001"} {
		writes := map[string]error{
			"Put":            svcs.KV.Put(ctx, key, []byte("x")),
			"PutWithMode":    svcs.KV.PutWithMode(ctx, key, []byte("x"), writemode.Sync),
			"Delete":         svcs.KV.Delete(ctx, key),
			"DeleteWithMode": svcs.KV.DeleteWithMode(ctx, key, writemode.Sync),
		}
		_, writes["PutIfAbsent"] = svcs.KV.PutIfAbsent(ctx, key, []byte("x"))
		_, writes["CompareAndSwap"] = svcs.KV.CompareAndSwap(ctx, key, []byte("x"), []byte("y"))
		_, writes["CompareAndDelete"] = svcs.KV.CompareAndDelete(ctx, key, []byte("x"))
		for op, err := range writes {
			var owned *service.SQLiteKeyError
			if !errors.As(err, &owned) || owned.Key != key {
				t.Errorf("%s(%q) = %v, want a SQLiteKeyError", op, key, err)
			}
		}
	}
	if len(kv.writes) != 0 {
		t.Fatalf("refused writes reached the store: %q", kv.writes)
	}
	for _, key := range []string{"app/meta", "sqlite", "sqlite/"} {
		if err := svcs.KV.Put(ctx, key, []byte("x")); err != nil {
			t.Errorf("Put(%q) outside a SQLite database = %v, want nil", key, err)
		}
	}
	if len(kv.writes) != 3 {
		t.Fatalf("writes outside a SQLite database reached the store %d times, want 3", len(kv.writes))
	}
}

func TestSQLiteKeyGuard_BatchWritesIntoADatabaseAreRefused(t *testing.T) {
	batch := &keyguardBatch{}
	svcs := withSQLiteKeyGuard(&DatabaseServices{KV: &keyguardKV{}, Batch: batch})
	ctx := context.Background()
	into := []service.Op{
		{Type: service.OpPut, Key: []byte("other"), Value: []byte("x")},
		{Type: service.OpPut, Key: []byte("sqlite/app/meta"), Value: []byte("x")},
	}
	var owned *service.SQLiteKeyError
	if _, err := svcs.Batch.Execute(ctx, into); !errors.As(err, &owned) {
		t.Errorf("Execute into a SQLite database = %v, want a SQLiteKeyError", err)
	}
	del := []service.Op{{Type: service.OpDelete, Key: []byte("sqlite/app/page/0001")}}
	if _, err := svcs.Batch.ExecuteWithMode(ctx, del, writemode.Sync); !errors.As(err, &owned) {
		t.Errorf("ExecuteWithMode delete in a SQLite database = %v, want a SQLiteKeyError", err)
	}
	if batch.batches != 0 {
		t.Fatalf("refused batches reached the store %d times", batch.batches)
	}
	outside := []service.Op{
		{Type: service.OpPut, Key: []byte("app/meta"), Value: []byte("x")},
		{Type: service.OpGet, Key: []byte("sqlite/app/meta")},
	}
	if _, err := svcs.Batch.Execute(ctx, outside); err != nil {
		t.Errorf("Execute outside a SQLite database = %v, want nil", err)
	}
	if batch.batches != 1 {
		t.Fatalf("batches outside a SQLite database reached the store %d times, want 1", batch.batches)
	}
}

func TestSQLiteKeyGuard_AdmissionGuardsKVAndBatch(t *testing.T) {
	svcs := withSQLiteAdmission(&DatabaseServices{KV: &keyguardKV{}, Batch: &keyguardBatch{}}, lockTenantA, (&admitFakes{allowed: true}).admission())
	var owned *service.SQLiteKeyError
	if err := svcs.KV.Put(context.Background(), "sqlite/app/meta", []byte("x")); !errors.As(err, &owned) {
		t.Fatalf("Put through the admitted services = %v, want a SQLiteKeyError", err)
	}
}

// TestSQLiteKeyGuard_MVCCWritesIntoADatabaseAreRefused covers the MVCC
// service's own writes on every scene: a write into a SQLite database is
// refused, one outside it lands, and the VFS still creates and commits.
func TestSQLiteKeyGuard_MVCCWritesIntoADatabaseAreRefused(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			svcs := gatedServices(t, build(t).registry, lockTenantA, &admitFakes{allowed: true, max: 4})
			w := gatedStore(t, svcs)
			if err := createDatabase(t, w, 0x11); err != nil {
				t.Fatalf("VFS create through the gated services: %v", err)
			}
			metaKey, err := sqlitevfs.MetaKey(wiringDB)
			if err != nil {
				t.Fatalf("MetaKey: %v", err)
			}
			ctx := t.Context()
			key := string(metaKey)
			_, putLease := svcs.MVCC.PutWithLease(ctx, key, []byte("x"), 0)
			_, putLeaseMode := svcs.MVCC.PutWithLeaseAndMode(ctx, key, []byte("x"), 0, writemode.Sync)
			writes := map[string]error{
				"Put":                 svcs.MVCC.Put(ctx, key, []byte("x")),
				"PutWithMode":         svcs.MVCC.PutWithMode(ctx, key, []byte("x"), writemode.Sync),
				"Delete":              svcs.MVCC.Delete(ctx, key),
				"DeleteWithMode":      svcs.MVCC.DeleteWithMode(ctx, key, writemode.Sync),
				"PutWithLease":        putLease,
				"PutWithLeaseAndMode": putLeaseMode,
			}
			_, writes["PutIfAbsent"] = svcs.MVCC.PutIfAbsent(ctx, key, []byte("x"))
			_, writes["CompareAndSwap"] = svcs.MVCC.CompareAndSwap(ctx, key, []byte("x"), []byte("y"))
			_, writes["CompareAndDelete"] = svcs.MVCC.CompareAndDelete(ctx, key, []byte("x"))
			for op, err := range writes {
				var owned *service.SQLiteKeyError
				if !errors.As(err, &owned) || owned.Key != key {
					t.Errorf("MVCC %s(%q) = %v, want a SQLiteKeyError", op, key, err)
				}
			}
			if _, err := svcs.MVCC.GetRevision(ctx, key); err != nil {
				t.Fatalf("meta record after the refused writes: %v", err)
			}
			if err := svcs.MVCC.Put(ctx, "app/meta", []byte("x")); err != nil {
				t.Fatalf("MVCC Put outside a SQLite database = %v, want nil", err)
			}
			if err := svcs.MVCC.Delete(ctx, "app/meta"); err != nil {
				t.Fatalf("MVCC Delete outside a SQLite database = %v, want nil", err)
			}
			txn, err := gatedStore(t, svcs).store.Begin(ctx)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			wiringWrite(t, txn, 0x21, 0x22)
			if _, err := txn.Commit(ctx); err != nil {
				t.Fatalf("VFS commit after the refused writes: %v", err)
			}
		})
	}
}
