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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// badPayload decodes successfully as nothing this suite's encoders produce,
// so every DecodeVectorXxx call in vector_effects.go hits its own decode
// error branch.
var badPayload = []byte{0xFF, 0xFF}

// stripHeader strips a full wire command's header (opcode and frame),
// returning the bare payload DecodeVectorXxx and the effect extractors
// expect -- the same payload UpdateEntries and KVEngine's dispatch pass
// them in production, after their own protocol.DecodeCommand call.
func stripHeader(t *testing.T, cmd []byte) []byte {
	t.Helper()
	decoded, err := protocol.DecodeCommand(cmd)
	if err != nil {
		t.Fatalf("DecodeCommand: %v", err)
	}
	return decoded.Payload
}

func TestVectorIndexEffects_UnknownOpReturnsNil(t *testing.T) {
	effects, err := vectorIndexEffects(nil, 0xEE, nil, sm.Result{}, nil)
	if err != nil {
		t.Fatalf("vectorIndexEffects(unknown op): %v", err)
	}
	if effects != nil {
		t.Fatalf("vectorIndexEffects(unknown op) = %v, want nil", effects)
	}
}

func TestVectorIndexEffects_UpdateMetadataHasNoEffect(t *testing.T) {
	effects, err := vectorIndexEffects(nil, protocol.OpVectorUpdateMetadata, nil, sm.Result{}, nil)
	if err != nil {
		t.Fatalf("vectorIndexEffects(UpdateMetadata): %v", err)
	}
	if effects != nil {
		t.Fatalf("vectorIndexEffects(UpdateMetadata) = %v, want nil: a metadata-only update never touches the embedding", effects)
	}
}

func TestEffectExtractors_DecodeErrorsPropagate(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	cases := []struct {
		name string
		op   byte
	}{
		{"NamespaceCreate", protocol.OpVectorNamespaceCreate},
		{"NamespaceDelete", protocol.OpVectorNamespaceDelete},
		{"Upsert", protocol.OpVectorUpsert},
		{"BatchUpsert", protocol.OpVectorBatchUpsert},
		{"Delete", protocol.OpVectorDelete},
		{"BatchDelete", protocol.OpVectorBatchDelete},
		{"IndexReconfigure", protocol.OpVectorIndexReconfigure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := vectorIndexEffects(batch, tc.op, badPayload, sm.Result{}, nil); err == nil {
				t.Fatalf("vectorIndexEffects(%s, bad payload) = nil error, want one", tc.name)
			}
		})
	}
}

func TestEffectsNamespaceCreate_MissingRecordReturnsNil(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	raw, err := protocol.EncodeVectorNamespaceCreate(protocol.VectorNamespaceCreateParams{Name: "never-created", Dimension: 1})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload := stripHeader(t, raw)
	effects, err := effectsNamespaceCreate(batch, payload)
	if err != nil {
		t.Fatalf("effectsNamespaceCreate: %v", err)
	}
	if effects != nil {
		t.Fatalf("effectsNamespaceCreate(no record in batch) = %v, want nil", effects)
	}
}

func TestEffectsReconfigure_MissingRecordReturnsNil(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	raw, err := protocol.EncodeVectorIndexReconfigure(protocol.VectorIndexReconfigureParams{Name: "never-created", IndexType: 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload := stripHeader(t, raw)
	effects, err := effectsReconfigure(batch, payload)
	if err != nil {
		t.Fatalf("effectsReconfigure: %v", err)
	}
	if effects != nil {
		t.Fatalf("effectsReconfigure(no record in batch) = %v, want nil", effects)
	}
}

func TestEffectsBatchUpsert_NilPreNextIDReturnsNil(t *testing.T) {
	raw, err := protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{Name: "ns", Vectors: []protocol.VectorUpsertParams{{Embedding: []float32{1}}}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload := stripHeader(t, raw)
	effects, err := effectsBatchUpsert(payload, nil)
	if err != nil {
		t.Fatalf("effectsBatchUpsert: %v", err)
	}
	if effects != nil {
		t.Fatalf("effectsBatchUpsert(nil preNextID) = %v, want nil", effects)
	}
}

func TestEffectsBatchUpsert_OverflowDuringReplayPropagatesError(t *testing.T) {
	raw, err := protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{Name: "ns", Vectors: []protocol.VectorUpsertParams{
		{Embedding: []float32{1}},
	}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload := stripHeader(t, raw)
	maxID := ^uint64(0)
	if _, err := effectsBatchUpsert(payload, &maxID); err == nil {
		t.Fatal("effectsBatchUpsert(preNextID=MaxUint64) = nil error, want an overflow refusal")
	}
}

func TestVectorBatchUpsertPreNextID_NonBatchUpsertOpReturnsNil(t *testing.T) {
	if got := vectorBatchUpsertPreNextID(nil, protocol.Command{Op: protocol.OpVectorUpsert}); got != nil {
		t.Fatalf("vectorBatchUpsertPreNextID(non-batch-upsert op) = %v, want nil", got)
	}
}

func TestVectorBatchUpsertPreNextID_BadPayloadReturnsNil(t *testing.T) {
	if got := vectorBatchUpsertPreNextID(nil, protocol.Command{Op: protocol.OpVectorBatchUpsert, Payload: badPayload}); got != nil {
		t.Fatalf("vectorBatchUpsertPreNextID(bad payload) = %v, want nil", got)
	}
}

func TestVectorBatchUpsertPreNextID_MissingNamespaceReturnsNil(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	batch := stg.Batch()
	defer func() { _ = batch.Close() }()

	raw, err := protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{Name: "never-created", Vectors: []protocol.VectorUpsertParams{{Embedding: []float32{1}}}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload := stripHeader(t, raw)
	if got := vectorBatchUpsertPreNextID(batch, protocol.Command{Op: protocol.OpVectorBatchUpsert, Payload: payload}); got != nil {
		t.Fatalf("vectorBatchUpsertPreNextID(missing namespace) = %v, want nil", got)
	}
}
