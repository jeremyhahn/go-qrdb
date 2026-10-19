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

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// errIndexFault is the error faultIndex returns from every mutation.
var errIndexFault = errors.New("store test: index write fault")

// faultIndex is an index.Index whose every mutation fails, so the apply
// paths' index-maintenance error branches can be driven deterministically.
type faultIndex struct{}

var _ index.Index = faultIndex{}

func (faultIndex) Put([]byte, []byte) error                         { return errIndexFault }
func (faultIndex) Get([]byte) ([][]byte, error)                     { return nil, nil }
func (faultIndex) Delete([]byte, []byte) error                      { return errIndexFault }
func (faultIndex) Scan([]byte, []byte) (map[string][][]byte, error) { return nil, nil }
func (faultIndex) GetAll() ([][]byte, error)                        { return nil, nil }
func (faultIndex) Clear() error                                     { return nil }
func (faultIndex) Close() error                                     { return nil }

// openIndexFaultStore opens a store with index by_status over "status",
// seeds user/1 with an indexed value, then swaps the real index for a
// faultIndex so the next index mutation fails.
func openIndexFaultStore(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := openExtendedStore(t)
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("by_status", "status", protocol.IndexCreateOptions{}))},
		{Index: 2, Cmd: mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"active"}`)))},
	})
	built, ok := s.indexes["by_status"]
	if !ok {
		t.Fatal("index by_status was not created")
	}
	if err := built.Close(); err != nil {
		t.Fatalf("closing the real index: %v", err)
	}
	s.indexes["by_status"] = faultIndex{}
	return s
}

// An index mutation that fails fails the entry on every write path, and the
// entry's write does not land: the stored value is still the seeded one.
func TestPebbleStateMachine_IndexFaultFailsTheEntry(t *testing.T) {
	cases := []struct {
		name string
		cmd  []byte
	}{
		{"put over an indexed value", mustEncode(kv.EncodePut([]byte("user/1"), []byte(`{"status":"retired"}`)))},
		{"put of a new indexed value", mustEncode(kv.EncodePut([]byte("user/2"), []byte(`{"status":"active"}`)))},
		{"delete of an indexed value", mustEncode(kv.EncodeDelete([]byte("user/1")))},
		{"matching compare-and-delete", mustEncode(protocol.EncodeCompareAndDelete([]byte("user/1"), []byte(`{"status":"active"}`)))},
		{"batch put", mustEncode(protocol.EncodeBatch([][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("user/1"), []byte(`{"status":"retired"}`), nil, 0)),
		}))},
		{"batch delete", mustEncode(protocol.EncodeBatch([][]byte{
			mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("user/1"), nil, nil, 0)),
		}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openIndexFaultStore(t)
			err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: tc.cmd}}, make([]sm.Result, 1))
			if !errors.Is(err, errIndexFault) {
				t.Fatalf("Update = %v, want the index fault propagated", err)
			}
			got, closer, gerr := s.db.Get([]byte("user/1"))
			if gerr != nil {
				t.Fatalf("Get(user/1) after the failed entry: %v", gerr)
			}
			value := string(got)
			_ = closer.Close()
			if value != `{"status":"active"}` {
				t.Fatalf("user/1 = %q after the failed entry, want the seeded value", value)
			}
		})
	}
}

// pebbleExecuteOp refuses an opcode it does not execute with a typed error,
// whatever validated the batch before it.
func TestPebbleExecuteOp_RefusesAnUnknownOpcode(t *testing.T) {
	for _, op := range []byte{0, 0xFF} {
		s := openExtendedStore(t)
		batch := s.db.NewIndexedBatch()
		_, err := pebbleExecuteOp(s, batch, protocol.DecodedOp{Type: op, Key: []byte("k")})
		_ = batch.Close()
		var unknown *protocol.UnknownOpError
		if !errors.As(err, &unknown) || unknown.Op != op {
			t.Fatalf("pebbleExecuteOp(op %#x) = %v, want *protocol.UnknownOpError naming it", op, err)
		}
	}
}

// A malformed compare-and-delete payload is refused with the decoder's error
// before anything is read or written.
func TestPebbleHandleCompareAndDelete_RefusesAMalformedPayload(t *testing.T) {
	for _, payload := range [][]byte{nil, {0xFF}} {
		s := openExtendedStore(t)
		batch := s.db.NewIndexedBatch()
		_, notifs, err := pebbleHandleCompareAndDelete(s, batch, payload)
		_ = batch.Close()
		if err == nil {
			t.Fatalf("payload %x: want a decode error", payload)
		}
		if notifs != nil {
			t.Fatalf("payload %x: notifications %v on a refused payload", payload, notifs)
		}
	}
}

// A reopened state machine whose audit chain head is malformed refuses to
// open, releases the database, and names the corruption.
func TestPebbleStateMachine_Open_RefusesACorruptAuditHead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	if err := db.Set([]byte(vlog.TablePrefix+"h/tenant-a"), []byte{0x01}, pebble.Sync); err != nil {
		t.Fatalf("seeding a malformed head: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("pebble Close: %v", err)
	}

	s := NewPebbleStateMachine(nil)
	s.SetLogs(vlog.NewChain())
	_, err = s.Open(context.Background(), dir, nil)
	var openErr *OpenError
	if !errors.As(err, &openErr) || openErr.Dir != dir {
		t.Fatalf("Open = %v, want *OpenError for %s", err, dir)
	}
	var corrupt *vlog.CorruptTableError
	if !errors.As(err, &corrupt) {
		t.Fatalf("Open = %v, want it to wrap *vlog.CorruptTableError", err)
	}
	if s.db != nil {
		t.Fatal("a refused Open left the database attached")
	}

	// The database was released: it opens again.
	db, err = pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("reopening after a refused Open: %v", err)
	}
	_ = db.Close()
}
