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

package sqlitevfs

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// RefuseKey refuses key with *service.SQLiteKeyError when it belongs to a
// SQLite database. Only the gated transaction service and the prefix move
// write a database's keys; every plain KV, MVCC and batch write refuses
// them, so a write cannot create a database without admission (row Q5) or
// tear one apart under its page store.
func RefuseKey(key []byte) error {
	if _, ok := DatabaseOfKey(key); ok {
		return &service.SQLiteKeyError{Key: string(key)}
	}
	return nil
}

// RefuseBatch refuses ops when one of its puts or deletes writes a SQLite
// key (see RefuseKey).
func RefuseBatch(ops []service.Op) error {
	for _, op := range ops {
		if op.Type != service.OpPut && op.Type != service.OpDelete {
			continue
		}
		if err := RefuseKey(op.Key); err != nil {
			return err
		}
	}
	return nil
}

// GuardKV returns kv refusing writes to SQLite keys. A nil kv stays nil and
// an already guarded kv is returned unchanged.
func GuardKV(kv service.KVServicer) service.KVServicer {
	if kv == nil {
		return nil
	}
	if _, guarded := kv.(KeyGuardKV); guarded {
		return kv
	}
	return KeyGuardKV{KVServicer: kv}
}

// GuardBatch returns b refusing batches and imports that write SQLite keys.
// A nil b stays nil and an already guarded b is returned unchanged.
func GuardBatch(b service.BatchServicer) service.BatchServicer {
	if b == nil {
		return nil
	}
	if _, guarded := b.(KeyGuardBatch); guarded {
		return b
	}
	return KeyGuardBatch{BatchServicer: b}
}

// KeyGuardKV is a KVServicer that refuses writes to SQLite keys.
type KeyGuardKV struct {
	service.KVServicer
}

// Put refuses a SQLite key, else delegates.
func (k KeyGuardKV) Put(ctx context.Context, key string, value []byte) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.Put(ctx, key, value)
}

// PutWithMode refuses a SQLite key, else delegates.
func (k KeyGuardKV) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.PutWithMode(ctx, key, value, mode)
}

// PutIfAbsent refuses a SQLite key, else delegates.
func (k KeyGuardKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return false, err
	}
	return k.KVServicer.PutIfAbsent(ctx, key, value)
}

// CompareAndSwap refuses a SQLite key, else delegates.
func (k KeyGuardKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return false, err
	}
	return k.KVServicer.CompareAndSwap(ctx, key, expected, next)
}

// Delete refuses a SQLite key, else delegates.
func (k KeyGuardKV) Delete(ctx context.Context, key string) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.Delete(ctx, key)
}

// DeleteWithMode refuses a SQLite key, else delegates.
func (k KeyGuardKV) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return k.KVServicer.DeleteWithMode(ctx, key, mode)
}

// CompareAndDelete refuses a SQLite key, else delegates.
func (k KeyGuardKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return false, err
	}
	return k.KVServicer.CompareAndDelete(ctx, key, expected)
}

// KeyGuardBatch is a BatchServicer that refuses batches and imports writing
// SQLite keys.
type KeyGuardBatch struct {
	service.BatchServicer
}

// Execute refuses a batch writing a SQLite key, else delegates.
func (b KeyGuardBatch) Execute(ctx context.Context, ops []service.Op) (*service.BatchResponse, error) {
	if err := RefuseBatch(ops); err != nil {
		return nil, err
	}
	return b.BatchServicer.Execute(ctx, ops)
}

// ExecuteWithMode refuses a batch writing a SQLite key, else delegates.
func (b KeyGuardBatch) ExecuteWithMode(ctx context.Context, ops []service.Op, mode writemode.Mode) (*service.BatchResponse, error) {
	if err := RefuseBatch(ops); err != nil {
		return nil, err
	}
	return b.BatchServicer.ExecuteWithMode(ctx, ops, mode)
}

// ImportCommit refuses every imported row whose key belongs to a SQLite
// database. Import proposes its rows directly (it never reaches Execute),
// so without this a tenant could import sqlite/<db>/meta or a page with no
// entitlement or count accounting and then build on it through a
// transaction (row Q5). The guard rides the request context, so it is
// checked on whichever node runs the commit, standalone or raft.
func (b KeyGuardBatch) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	return b.BatchServicer.ImportCommit(service.WithImportRowGuard(ctx, func(key string) error {
		return RefuseKey([]byte(key))
	}), sessionID)
}
