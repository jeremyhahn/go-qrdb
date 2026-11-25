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

package service

import (
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// orderedUniqueGatedHost is a FeatureGatedHost that answers
// ordered-unique-check with active, records every feature asked about and
// keeps the commands proposed through it.
type orderedUniqueGatedHost struct {
	*mockHost
	active bool
	asked  []feature.Feature
}

func (h *orderedUniqueGatedHost) FeatureEnabled(_ uint64, f feature.Feature) bool {
	h.asked = append(h.asked, f)
	return h.active
}

// newRecordingHost returns a mockHost whose proposals are appended to
// *proposed and answered with a succeeded transaction / applied batch.
func newRecordingHost(proposed *[][]byte) *mockHost {
	return &mockHost{
		syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
			*proposed = append(*proposed, slices.Clone(cmd))
			return sm.Result{Value: 1, Data: []byte{1}}, nil
		},
	}
}

// proposedFlags decodes the flag trailer of each proposed OpTransaction or
// OpBatch command.
func proposedFlags(t *testing.T, cmds [][]byte) []byte {
	t.Helper()
	var flags []byte
	for _, cmd := range cmds {
		decoded, err := protocol.DecodeCommand(cmd)
		if err != nil {
			t.Fatalf("decode command: %v", err)
		}
		switch decoded.Op {
		case protocol.OpTransaction:
			txn, err := protocol.DecodeTransaction(decoded.Payload)
			if err != nil {
				t.Fatalf("decode transaction: %v", err)
			}
			flags = append(flags, txn.Flags)
		case protocol.OpBatch:
			_, f, err := protocol.DecodeBatchFlags(decoded.Payload)
			if err != nil {
				t.Fatalf("decode batch: %v", err)
			}
			flags = append(flags, f)
		default:
			t.Fatalf("proposed op %d, want a transaction or batch", decoded.Op)
		}
	}
	return flags
}

func uniqueGateOps() []Op {
	return []Op{
		{Type: OpPut, Key: []byte("user:1"), Value: []byte(`{"name":"y"}`)},
		{Type: OpPut, Key: []byte("user:2"), Value: []byte(`{"name":"x"}`)},
	}
}

func runTransactionUnderGate(t *testing.T, host Host) {
	t.Helper()
	svc := NewTransactionService(host, 1)
	if _, err := svc.Execute(context.Background(), nil, uniqueGateOps(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := svc.ExecuteEntity(context.Background(), &TransactionEntityRequest{
		Success: []TransactionEntityOp{{Type: "put", EntityType: "user", ID: "u1", Data: []byte(`{"name":"x"}`)}},
	}); err != nil {
		t.Fatalf("ExecuteEntity: %v", err)
	}
}

func runBatchUnderGate(t *testing.T, host Host) {
	t.Helper()
	svc := NewBatchService(host, 1)
	if _, err := svc.Execute(context.Background(), uniqueGateOps()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := svc.ExecuteEntity(context.Background(), &BatchEntityRequest{
		Ops: []BatchEntityOp{{Type: "put", EntityType: "user", ID: "u1", Data: []byte(`{"name":"x"}`)}},
	}); err != nil {
		t.Fatalf("ExecuteEntity: %v", err)
	}
}

func TestTransactionService_OrderedUniqueNotEnabled_ProposesLegacy(t *testing.T) {
	var proposed [][]byte
	host := &orderedUniqueGatedHost{mockHost: newRecordingHost(&proposed)}
	runTransactionUnderGate(t, host)
	if got := proposedFlags(t, proposed); !slices.Equal(got, []byte{0, 0}) {
		t.Fatalf("proposed flags = %v, want [0 0] (legacy bytes)", got)
	}
	if !slices.Contains(host.asked, featuregate.OrderedUniqueCheck) {
		t.Fatalf("asked %v, want %s", host.asked, featuregate.OrderedUniqueCheck)
	}
}

func TestTransactionService_OrderedUniqueEnabled_ProposesFlag(t *testing.T) {
	var proposed [][]byte
	host := &orderedUniqueGatedHost{mockHost: newRecordingHost(&proposed), active: true}
	runTransactionUnderGate(t, host)
	want := []byte{protocol.CmdFlagOrderedUnique, protocol.CmdFlagOrderedUnique}
	if got := proposedFlags(t, proposed); !slices.Equal(got, want) {
		t.Fatalf("proposed flags = %v, want %v", got, want)
	}
}

func TestBatchService_OrderedUniqueNotEnabled_ProposesLegacy(t *testing.T) {
	var proposed [][]byte
	host := &orderedUniqueGatedHost{mockHost: newRecordingHost(&proposed)}
	runBatchUnderGate(t, host)
	if got := proposedFlags(t, proposed); !slices.Equal(got, []byte{0, 0}) {
		t.Fatalf("proposed flags = %v, want [0 0] (legacy bytes)", got)
	}
	if !slices.Contains(host.asked, featuregate.OrderedUniqueCheck) {
		t.Fatalf("asked %v, want %s", host.asked, featuregate.OrderedUniqueCheck)
	}
}

func TestBatchService_OrderedUniqueEnabled_ProposesFlag(t *testing.T) {
	var proposed [][]byte
	host := &orderedUniqueGatedHost{mockHost: newRecordingHost(&proposed), active: true}
	runBatchUnderGate(t, host)
	want := []byte{protocol.CmdFlagOrderedUnique, protocol.CmdFlagOrderedUnique}
	if got := proposedFlags(t, proposed); !slices.Equal(got, want) {
		t.Fatalf("proposed flags = %v, want %v", got, want)
	}
}

// A host that gates nothing (standalone, embedded) runs one binary, so the
// flag is proposed at once.
func TestTransactionService_OrderedUniqueUngatedHost_ProposesFlag(t *testing.T) {
	var proposed [][]byte
	runTransactionUnderGate(t, newRecordingHost(&proposed))
	runBatchUnderGate(t, newRecordingHost(&proposed))
	want := []byte{protocol.CmdFlagOrderedUnique, protocol.CmdFlagOrderedUnique, protocol.CmdFlagOrderedUnique, protocol.CmdFlagOrderedUnique}
	if got := proposedFlags(t, proposed); !slices.Equal(got, want) {
		t.Fatalf("proposed flags = %v, want %v", got, want)
	}
}
