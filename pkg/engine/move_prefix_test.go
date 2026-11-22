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

package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// movePrefixEntry encodes an OpMVCCMovePrefix entry; extra holds the extra
// ops as key/value pairs, an empty value meaning a delete.
func movePrefixEntry(t *testing.T, idx uint64, src, dst string, expected int64, extra ...[2]string) sm.Entry {
	t.Helper()
	ops := make([][]byte, 0, len(extra))
	for _, kv := range extra {
		var op []byte
		var err error
		if kv[1] == "" {
			op, err = protocol.EncodeOp(protocol.OpMVCCDelete, []byte(kv[0]), nil, nil, 0)
		} else {
			op, err = protocol.EncodeOp(protocol.OpMVCCPut, []byte(kv[0]), []byte(kv[1]), nil, 0)
		}
		if err != nil {
			t.Fatalf("EncodeOp: %v", err)
		}
		ops = append(ops, op)
	}
	cmd, err := protocol.EncodeMVCCMovePrefix([]byte(src), []byte(dst), expected, ops)
	if err != nil {
		t.Fatalf("EncodeMVCCMovePrefix: %v", err)
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

func mvccValue(t *testing.T, eng *MVCCEngine, key string) (string, int64) {
	t.Helper()
	kv, err := eng.Store().Get([]byte(key))
	if err != nil || kv == nil {
		return "", 0
	}
	return string(kv.Value), kv.ModRevision
}

func TestMVCCEngine_MovePrefix_PublishesAtOneRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "db/s/1/a", "db/s/1/b", "db/s/1/cc", "db/p/a", "db/p/z", "db/meta", "db/s/2/x")
	before := eng.Store().CurrentRevision()

	results := applyMVCCEntries(t, eng, []sm.Entry{
		movePrefixEntry(t, 20, "db/s/1/", "db/p/", 3, [2]string{"db/meta", "m2"}, [2]string{"db/p/z", ""}),
	})
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("move refused: %+v", results[0])
	}
	rev := before + 1
	if got := eng.Store().CurrentRevision(); got != rev {
		t.Fatalf("revision = %d, want %d: a move is one revision", got, rev)
	}
	if results[0].Value != uint64(rev) {
		t.Fatalf("result Value = %d, want the revision %d", results[0].Value, rev)
	}
	for _, k := range []string{"db/s/1/a", "db/s/1/b", "db/s/1/cc", "db/p/z"} {
		if mvccLive(t, eng, k) {
			t.Errorf("key %q survived the move", k)
		}
	}
	// db/p/a already existed: the move overwrites it.
	for _, suffix := range []string{"a", "b", "cc"} {
		v, mod := mvccValue(t, eng, "db/p/"+suffix)
		if v != "v-db/s/1/"+suffix || mod != rev {
			t.Errorf("db/p/%s = %q at %d, want the staged value at %d", suffix, v, mod, rev)
		}
	}
	if v, mod := mvccValue(t, eng, "db/meta"); v != "m2" || mod != rev {
		t.Errorf("meta = %q at %d, want m2 at %d", v, mod, rev)
	}
	if !mvccLive(t, eng, "db/s/2/x") {
		t.Error("a key under another stage was moved")
	}
}

func TestMVCCEngine_MovePrefix_CountMismatchWritesNothing(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "s/a", "s/b", "meta")
	before := eng.Store().CurrentRevision()

	results := applyMVCCEntries(t, eng, []sm.Entry{movePrefixEntry(t, 20, "s/", "p/", 3, [2]string{"meta", "m2"})})
	msg, refused := protocol.DecodeAppError(results[0].Data)
	if !refused || !strings.Contains(msg, "expected 3") {
		t.Fatalf("result = %+v, want the conflict refusal", results[0])
	}
	if !strings.HasPrefix(string(results[0].Data), string(protocol.EncodeAppError(protocol.AppErrorConflict, ""))) {
		t.Fatalf("refusal kind is not AppErrorConflict: %q", results[0].Data)
	}
	if got := eng.Store().CurrentRevision(); got != before {
		t.Fatalf("revision = %d, want %d: a refused move writes nothing", got, before)
	}
	if !mvccLive(t, eng, "s/a") || mvccLive(t, eng, "p/a") {
		t.Fatal("a refused move moved a key")
	}
	if v, _ := mvccValue(t, eng, "meta"); v != "v-meta" {
		t.Fatalf("meta = %q: a refused move applied an extra op", v)
	}
}

func TestMVCCEngine_MovePrefix_SkipsKeysDeletedInHistory(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "s/a", "s/b")
	applyMVCCEntries(t, eng, []sm.Entry{mvccDeleteEntry(10, []byte("s/b"))})

	results := applyMVCCEntries(t, eng, []sm.Entry{movePrefixEntry(t, 20, "s/", "p/", 1)})
	if _, refused := protocol.DecodeAppError(results[0].Data); refused {
		t.Fatalf("move refused: %+v", results[0])
	}
	if !mvccLive(t, eng, "p/a") || mvccLive(t, eng, "p/b") {
		t.Fatal("the move did not move exactly the live key")
	}
}

func TestMVCCEngine_MovePrefix_RefusesReservedAndOverlap(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()
	seedMVCC(t, eng, "s/a", "s/b/c")
	before := eng.Store().CurrentRevision()

	entries := []sm.Entry{
		movePrefixEntry(t, 20, "\x00r/", "p/", 1),
		movePrefixEntry(t, 21, "s/", "\x00r/", 2),
		movePrefixEntry(t, 22, "s/", "p/", 2, [2]string{"\x00meta", "m"}),
		movePrefixEntry(t, 23, "s/", "s/b/", 2),
		movePrefixEntry(t, 24, "s/b/", "s/", 1),
	}
	for i, res := range applyMVCCEntries(t, eng, entries) {
		if _, refused := protocol.DecodeAppError(res.Data); !refused {
			t.Errorf("entry %d not refused: %+v", i, res)
		}
	}
	if got := eng.Store().CurrentRevision(); got != before {
		t.Fatalf("revision = %d, want %d: refused moves write nothing", got, before)
	}
	if !mvccLive(t, eng, "s/a") || !mvccLive(t, eng, "s/b/c") {
		t.Fatal("a refused move moved a key")
	}
}
