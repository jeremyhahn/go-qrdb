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
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// ---------------------------------------------------------------------------
// Index effects: the post-commit half of docs/vectors/engineering.md "Apply
// and the index".
//
// vectorIndexEffects observes what a vector command that has ALREADY applied
// successfully did to the store, and reports it as IndexEffects for the
// engine's VectorIndexManager to apply to the in-memory ANN index once the
// batch that produced them has committed. It is deliberately NOT part of
// vector_handlers.go or the handler dispatch tables: it needs only the
// op code, the undecoded payload, the batch (to read state the command just
// staged) and the command's Result -- the same four things
// MVCCEngine.UpdateEntries and KVEngine's apply path already have for every
// entry, vector or not. Adding it here, rather than threading a fifth return
// value through every vector apply function and both engines' handler
// function types, keeps vector_handlers.go and its ~100 existing direct
// call-site tests (which assert the two-value (sm.Result, error) shape)
// completely unchanged.
//
// Every read here goes through the SAME batch the command staged into,
// before that batch commits, so it sees exactly what the command just
// wrote -- not a later or earlier view. A decode or read failure here is a
// FAULT, not a refusal: the command already validated and committed
// successfully, so the identical decode that handler just ran (or this
// function itself, on a value the handler already proved well-formed)
// failing now is a bug, not a bad request.
// ---------------------------------------------------------------------------

// vectorBatchUpsertPreNextID reads namespace's NextID through batch BEFORE
// cmd's handler runs, returning nil for every opcode except
// protocol.OpVectorBatchUpsert. A batch upsert's Result carries only an item
// count, not which ID each item was assigned, so reconstructing those IDs
// after the fact requires replaying allocateID from the SAME starting
// NextID applyVectorBatchUpsert itself started from -- which is the
// namespace's NextID as it stood immediately before this command, not after
// (every other vector command's effect is derivable from its own payload
// and Result alone, without a pre-image).
func vectorBatchUpsertPreNextID(batch storage.Batch, cmd protocol.Command) *uint64 {
	if cmd.Op != protocol.OpVectorBatchUpsert {
		return nil
	}
	p, err := protocol.DecodeVectorBatchUpsert(cmd.Payload)
	if err != nil {
		return nil // the handler will hit the identical decode error
	}
	rec, err := readNamespaceRecord(batch, p.Name)
	if err != nil || rec == nil {
		return nil
	}
	n := rec.NextID
	return &n
}

// vectorIndexEffects returns the IndexEffect(s) op's successful apply
// produced. It returns (nil, nil) for an opcode with no index effect
// (protocol.OpVectorUpdateMetadata never changes an embedding) and for any
// opcode this function does not recognise.
func vectorIndexEffects(batch storage.Batch, op byte, payload []byte, result sm.Result, preNextID *uint64) ([]vector.IndexEffect, error) {
	switch op {
	case protocol.OpVectorNamespaceCreate:
		return effectsNamespaceCreate(batch, payload)
	case protocol.OpVectorNamespaceDelete:
		return effectsNamespaceDelete(payload)
	case protocol.OpVectorUpsert:
		return effectsUpsert(payload, result)
	case protocol.OpVectorBatchUpsert:
		return effectsBatchUpsert(payload, preNextID)
	case protocol.OpVectorDelete:
		return effectsDelete(payload)
	case protocol.OpVectorBatchDelete:
		return effectsBatchDelete(payload)
	case protocol.OpVectorIndexReconfigure:
		return effectsReconfigure(batch, payload)
	default:
		return nil, nil
	}
}

func effectsNamespaceCreate(batch storage.Batch, payload []byte) ([]vector.IndexEffect, error) {
	p, err := protocol.DecodeVectorNamespaceCreate(payload)
	if err != nil {
		return nil, err
	}
	rec, err := readNamespaceRecord(batch, p.Name)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	return []vector.IndexEffect{{Op: vector.IndexEffectNamespaceCreate, Namespace: p.Name, Record: *rec}}, nil
}

func effectsNamespaceDelete(payload []byte) ([]vector.IndexEffect, error) {
	namespace, err := protocol.DecodeVectorNamespaceDelete(payload)
	if err != nil {
		return nil, err
	}
	return []vector.IndexEffect{{Op: vector.IndexEffectNamespaceDelete, Namespace: namespace}}, nil
}

func effectsUpsert(payload []byte, result sm.Result) ([]vector.IndexEffect, error) {
	namespace, _, item, err := protocol.DecodeVectorUpsert(payload)
	if err != nil {
		return nil, err
	}
	return []vector.IndexEffect{{Op: vector.IndexEffectUpsert, Namespace: namespace, ID: result.Value, Embedding: item.Embedding}}, nil
}

func effectsBatchUpsert(payload []byte, preNextID *uint64) ([]vector.IndexEffect, error) {
	p, err := protocol.DecodeVectorBatchUpsert(payload)
	if err != nil {
		return nil, err
	}
	if preNextID == nil {
		// The pre-read could not establish a starting NextID (a decode or
		// read failure that will have surfaced, identically, from the
		// handler's own pass -- this command cannot have committed without
		// it). Nothing safe to report.
		return nil, nil
	}
	nextID := *preNextID
	effects := make([]vector.IndexEffect, 0, len(p.Vectors))
	for _, item := range p.Vectors {
		id, newNextID, err := allocateID(p.Name, nextID, item)
		if err != nil {
			return nil, err
		}
		nextID = newNextID
		effects = append(effects, vector.IndexEffect{Op: vector.IndexEffectUpsert, Namespace: p.Name, ID: id, Embedding: item.Embedding})
	}
	return effects, nil
}

func effectsDelete(payload []byte) ([]vector.IndexEffect, error) {
	namespace, id, err := protocol.DecodeVectorDelete(payload)
	if err != nil {
		return nil, err
	}
	return []vector.IndexEffect{{Op: vector.IndexEffectDelete, Namespace: namespace, ID: id}}, nil
}

func effectsBatchDelete(payload []byte) ([]vector.IndexEffect, error) {
	namespace, ids, err := protocol.DecodeVectorBatchDelete(payload)
	if err != nil {
		return nil, err
	}
	effects := make([]vector.IndexEffect, 0, len(ids))
	for _, id := range ids {
		effects = append(effects, vector.IndexEffect{Op: vector.IndexEffectDelete, Namespace: namespace, ID: id})
	}
	return effects, nil
}

func effectsReconfigure(batch storage.Batch, payload []byte) ([]vector.IndexEffect, error) {
	p, err := protocol.DecodeVectorIndexReconfigure(payload)
	if err != nil {
		return nil, err
	}
	rec, err := readNamespaceRecord(batch, p.Name)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	return []vector.IndexEffect{{Op: vector.IndexEffectReconfigure, Namespace: p.Name, Record: *rec}}, nil
}
