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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// recordingKV is a KVServicer double that records which write reached it;
// methods it does not override panic through the nil embedded interface, so
// a guard delegating to an unexpected method fails the test loudly.
type recordingKV struct {
	service.KVServicer
	calls []string
}

func (r *recordingKV) Put(_ context.Context, key string, _ []byte) error {
	r.calls = append(r.calls, "Put "+key)
	return nil
}

func (r *recordingKV) PutWithMode(_ context.Context, key string, _ []byte, _ writemode.Mode) error {
	r.calls = append(r.calls, "PutWithMode "+key)
	return nil
}

func (r *recordingKV) PutIfAbsent(_ context.Context, key string, _ []byte) (bool, error) {
	r.calls = append(r.calls, "PutIfAbsent "+key)
	return true, nil
}

func (r *recordingKV) CompareAndSwap(_ context.Context, key string, _, _ []byte) (bool, error) {
	r.calls = append(r.calls, "CompareAndSwap "+key)
	return true, nil
}

func (r *recordingKV) Delete(_ context.Context, key string) error {
	r.calls = append(r.calls, "Delete "+key)
	return nil
}

func (r *recordingKV) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	r.calls = append(r.calls, "DeleteWithMode "+key)
	return nil
}

func (r *recordingKV) CompareAndDelete(_ context.Context, key string, _ []byte) (bool, error) {
	r.calls = append(r.calls, "CompareAndDelete "+key)
	return true, nil
}

// recordingBatch is a BatchServicer double recording delegated calls.
type recordingBatch struct {
	service.BatchServicer
	calls     []string
	importCtx context.Context
}

func (r *recordingBatch) Execute(_ context.Context, ops []service.Op) (*service.BatchResponse, error) {
	r.calls = append(r.calls, "Execute")
	return &service.BatchResponse{}, nil
}

func (r *recordingBatch) ExecuteWithMode(_ context.Context, ops []service.Op, _ writemode.Mode) (*service.BatchResponse, error) {
	r.calls = append(r.calls, "ExecuteWithMode")
	return &service.BatchResponse{}, nil
}

func (r *recordingBatch) ImportCommit(ctx context.Context, sessionID string) (*service.ImportResult, error) {
	r.calls = append(r.calls, "ImportCommit "+sessionID)
	r.importCtx = ctx
	return &service.ImportResult{}, nil
}

func TestRefuseKey(t *testing.T) {
	meta, err := MetaKey("app")
	if err != nil {
		t.Fatalf("MetaKey: %v", err)
	}
	cases := []struct {
		name   string
		key    []byte
		refuse bool
	}{
		{"meta key", meta, true},
		{"page key", []byte("sqlite/app/p/x"), true},
		{"plain key", []byte("plain"), false},
		{"root only", []byte("sqlite/"), false},
		{"no database segment end", []byte("sqlite/app"), false},
		{"empty database name", []byte("sqlite//meta"), false},
		{"dot-dot database name", []byte("sqlite/../meta"), false},
		{"control char database name", []byte("sqlite/a\x01b/meta"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RefuseKey(tc.key)
			var keyErr *service.SQLiteKeyError
			if tc.refuse {
				if !errors.As(err, &keyErr) || keyErr.Key != string(tc.key) {
					t.Fatalf("RefuseKey(%q) = %v, want SQLiteKeyError on the key", tc.key, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RefuseKey(%q) = %v, want nil", tc.key, err)
			}
		})
	}
}

func TestRefuseBatch(t *testing.T) {
	cases := []struct {
		name   string
		ops    []service.Op
		refuse bool
	}{
		{"empty", nil, false},
		{"plain put and delete", []service.Op{{Type: service.OpPut, Key: []byte("a")}, {Type: service.OpDelete, Key: []byte("b")}}, false},
		{"sqlite get is a read", []service.Op{{Type: service.OpGet, Key: []byte("sqlite/app/meta")}}, false},
		{"sqlite range is a read", []service.Op{{Type: service.OpRange, Key: []byte("sqlite/app/meta")}}, false},
		{"sqlite put", []service.Op{{Type: service.OpPut, Key: []byte("a")}, {Type: service.OpPut, Key: []byte("sqlite/app/meta")}}, true},
		{"sqlite delete", []service.Op{{Type: service.OpDelete, Key: []byte("sqlite/app/meta")}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RefuseBatch(tc.ops)
			var keyErr *service.SQLiteKeyError
			if tc.refuse != errors.As(err, &keyErr) {
				t.Fatalf("RefuseBatch = %v, refuse want %v", err, tc.refuse)
			}
			if !tc.refuse && err != nil {
				t.Fatalf("RefuseBatch = %v, want nil", err)
			}
		})
	}
}

func TestGuardKV_WrapsOnce(t *testing.T) {
	if GuardKV(nil) != nil {
		t.Fatal("GuardKV(nil) != nil")
	}
	inner := &recordingKV{}
	g := GuardKV(inner)
	kg, ok := g.(KeyGuardKV)
	if !ok || kg.KVServicer != inner {
		t.Fatalf("GuardKV = %T, want KeyGuardKV over the inner kv", g)
	}
	if again := GuardKV(g); again != g {
		t.Fatal("GuardKV re-wrapped an already guarded kv")
	}
}

func TestGuardBatch_WrapsOnce(t *testing.T) {
	if GuardBatch(nil) != nil {
		t.Fatal("GuardBatch(nil) != nil")
	}
	inner := &recordingBatch{}
	g := GuardBatch(inner)
	kb, ok := g.(KeyGuardBatch)
	if !ok || kb.BatchServicer != inner {
		t.Fatalf("GuardBatch = %T, want KeyGuardBatch over the inner batch", g)
	}
	if again := GuardBatch(g); again != g {
		t.Fatal("GuardBatch re-wrapped an already guarded batch")
	}
}

func TestKeyGuardKV_RefusesSQLiteKeysAndDelegatesOthers(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name string
		call func(kv service.KVServicer, key string) error
	}{
		{"Put", func(kv service.KVServicer, key string) error { return kv.Put(ctx, key, []byte("v")) }},
		{"PutWithMode", func(kv service.KVServicer, key string) error {
			return kv.PutWithMode(ctx, key, []byte("v"), writemode.Mode(0))
		}},
		{"PutIfAbsent", func(kv service.KVServicer, key string) error {
			_, err := kv.PutIfAbsent(ctx, key, []byte("v"))
			return err
		}},
		{"CompareAndSwap", func(kv service.KVServicer, key string) error {
			_, err := kv.CompareAndSwap(ctx, key, []byte("o"), []byte("n"))
			return err
		}},
		{"Delete", func(kv service.KVServicer, key string) error { return kv.Delete(ctx, key) }},
		{"DeleteWithMode", func(kv service.KVServicer, key string) error {
			return kv.DeleteWithMode(ctx, key, writemode.Mode(0))
		}},
		{"CompareAndDelete", func(kv service.KVServicer, key string) error {
			_, err := kv.CompareAndDelete(ctx, key, []byte("o"))
			return err
		}},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			inner := &recordingKV{}
			kv := GuardKV(inner)
			err := w.call(kv, "sqlite/app/meta")
			var keyErr *service.SQLiteKeyError
			if !errors.As(err, &keyErr) || keyErr.Key != "sqlite/app/meta" {
				t.Fatalf("%s(sqlite key) = %v, want SQLiteKeyError", w.name, err)
			}
			if len(inner.calls) != 0 {
				t.Fatalf("refused write reached the inner kv: %v", inner.calls)
			}
			if err := w.call(kv, "plain"); err != nil {
				t.Fatalf("%s(plain) = %v", w.name, err)
			}
			if len(inner.calls) != 1 || inner.calls[0] != w.name+" plain" {
				t.Fatalf("inner calls = %v, want [%s plain]", inner.calls, w.name)
			}
		})
	}
}

func TestKeyGuardBatch_RefusesSQLiteKeysAndDelegatesOthers(t *testing.T) {
	ctx := context.Background()
	execs := []struct {
		name string
		call func(b service.BatchServicer, ops []service.Op) error
	}{
		{"Execute", func(b service.BatchServicer, ops []service.Op) error {
			_, err := b.Execute(ctx, ops)
			return err
		}},
		{"ExecuteWithMode", func(b service.BatchServicer, ops []service.Op) error {
			_, err := b.ExecuteWithMode(ctx, ops, writemode.Mode(0))
			return err
		}},
	}
	refused := []service.Op{{Type: service.OpPut, Key: []byte("plain")}, {Type: service.OpDelete, Key: []byte("sqlite/app/meta")}}
	allowed := []service.Op{{Type: service.OpPut, Key: []byte("plain")}, {Type: service.OpGet, Key: []byte("sqlite/app/meta")}}
	for _, e := range execs {
		t.Run(e.name, func(t *testing.T) {
			inner := &recordingBatch{}
			b := GuardBatch(inner)
			var keyErr *service.SQLiteKeyError
			if err := e.call(b, refused); !errors.As(err, &keyErr) {
				t.Fatalf("%s(sqlite delete) = %v, want SQLiteKeyError", e.name, err)
			}
			if len(inner.calls) != 0 {
				t.Fatalf("refused batch reached the inner batch: %v", inner.calls)
			}
			if err := e.call(b, allowed); err != nil {
				t.Fatalf("%s(allowed) = %v", e.name, err)
			}
			if len(inner.calls) != 1 || inner.calls[0] != e.name {
				t.Fatalf("inner calls = %v, want [%s]", inner.calls, e.name)
			}
		})
	}
}

type keyguardCtxKey struct{}

func TestKeyGuardBatch_ImportCommitDelegatesWithCallerContext(t *testing.T) {
	inner := &recordingBatch{}
	b := GuardBatch(inner)
	ctx := context.WithValue(context.Background(), keyguardCtxKey{}, "caller")
	if _, err := b.ImportCommit(ctx, "sess-1"); err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "ImportCommit sess-1" {
		t.Fatalf("inner calls = %v, want [ImportCommit sess-1]", inner.calls)
	}
	if inner.importCtx == ctx {
		t.Fatal("ImportCommit passed the caller context through without the row guard")
	}
	if got := inner.importCtx.Value(keyguardCtxKey{}); got != "caller" {
		t.Fatalf("import context lost the caller's values: %v", got)
	}
}
