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
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func TestMVCCEngine_TransactionRead_CorruptMetadataFailsTheTransaction(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})
	before := mvccRevision(t, eng)
	corrupt := stg.Batch()
	if err := corrupt.Set(mvcc.EncodeMetaKey([]byte("k")), []byte{0xff}); err != nil {
		t.Fatalf("corrupt the metadata entry: %v", err)
	}
	if err := corrupt.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = corrupt.Close()

	for _, read := range [][]byte{
		txnGetOp("k"),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCRange, []byte("a"), nil, []byte("z"), 0)),
	} {
		cmd := mustEncode(protocol.EncodeTransaction(nil, [][]byte{txnPutOp("other", []byte("o")), read}, nil))
		results := make([]sm.Result, 1)
		err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: cmd}}, results)
		var nf *mvcc.KeyNotFoundError
		if err == nil || errors.As(err, &nf) {
			t.Fatalf("Update = %v (result %q), want the metadata fault to fail the transaction", err, results[0].Data)
		}
		if bytes.HasPrefix(results[0].Data, []byte{1}) {
			t.Fatalf("result %q reports a committed transaction over corrupt metadata", results[0].Data)
		}
		assertNotWritten(t, eng, "other")
		if after := mvccRevision(t, eng); after != before {
			t.Fatalf("revision moved from %d to %d on a failed transaction", before, after)
		}
	}
}
