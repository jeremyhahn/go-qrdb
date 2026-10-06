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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// checkpointFormatVersion is the on-disk envelope format Checkpoint writes
// and loadCheckpoint reads. A checkpoint whose version does not match this
// build's is never trusted (see loadCheckpoint): the safe behaviour for an
// unreadable or unrecognised checkpoint is always a rebuild, never an error
// (docs/vectors/engineering.md "Checkpoints", "A memory binding writes
// none" and the no-node-local-state rule this phase was built under).
const checkpointFormatVersion = 1

// checkpointSuffix names a namespace's checkpoint files under
// <dir>/vector-index/<namespace>/.
const checkpointSuffix = ".idx"

// vectorIndexDir is the fixed subdirectory name under the binding's data
// root every namespace's checkpoints live under
// (docs/vectors/engineering.md "Checkpoints", "Path").
const vectorIndexDir = "vector-index"

// encodeCheckpointBody serializes a checkpoint's envelope: format version,
// namespace sequence, index type, training generation, index parameters,
// then the live index's own serialized bytes. Encryption is applied by the
// vfs.FS this is written through (see writeCheckpoint), not by this
// function: the envelope itself is plaintext bytes handed to that FS.
func encodeCheckpointBody(seq uint64, indexType byte, params map[string]string, generation uint64, indexPayload []byte) ([]byte, error) {
	paramSize, err := protocol.ParamBlockSize(params)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1+8+1+8+paramSize+len(indexPayload))
	off := 0
	buf[off] = checkpointFormatVersion
	off++
	binary.BigEndian.PutUint64(buf[off:], seq)
	off += 8
	buf[off] = indexType
	off++
	binary.BigEndian.PutUint64(buf[off:], generation)
	off += 8
	off, err = protocol.WriteParamBlock(buf, off, params)
	if err != nil {
		return nil, err
	}
	copy(buf[off:], indexPayload)
	return buf, nil
}

// checkpointHeader is decodeCheckpointBody's result.
type checkpointHeader struct {
	version    byte
	seq        uint64
	indexType  byte
	generation uint64
	params     map[string]string
	payload    []byte
}

// decodeCheckpointBody is encodeCheckpointBody's inverse.
func decodeCheckpointBody(data []byte) (checkpointHeader, error) {
	if len(data) < 18 {
		return checkpointHeader{}, &vector.InvalidRecordError{Record: "vector checkpoint", Reason: "too short"}
	}
	off := 0
	version := data[off]
	off++
	seq := binary.BigEndian.Uint64(data[off:])
	off += 8
	indexType := data[off]
	off++
	generation := binary.BigEndian.Uint64(data[off:])
	off += 8
	params, off, err := protocol.ReadParamBlock(data, off, "vector checkpoint")
	if err != nil {
		return checkpointHeader{}, err
	}
	return checkpointHeader{
		version:    version,
		seq:        seq,
		indexType:  indexType,
		generation: generation,
		params:     params,
		payload:    data[off:],
	}, nil
}

// fs returns the vfs.FS checkpoint I/O goes through: the binding's own
// encryptor when one is configured, transparently encrypting every
// checkpoint at rest (it holds every embedding in the namespace), or the
// plain base FS when the encryptor is disabled (a nil barrier, e.g. in
// tests).
func (m *VectorIndexManager) fs() vfs.FS {
	return m.enc.FS()
}

func (m *VectorIndexManager) namespaceDir(fsys vfs.FS, namespace string) string {
	return fsys.PathJoin(m.dir, vectorIndexDir, namespace)
}

// writeCheckpoint writes namespace's checkpoint to a temporary name, syncs
// it, renames it into place, and removes every other checkpoint file for
// the namespace (docs/vectors/engineering.md "Checkpoints", "Path").
func (m *VectorIndexManager) writeCheckpoint(namespace string, rec vector.NamespaceRecord, live vector.LiveIndex) error {
	payload, err := live.Marshal()
	if err != nil {
		if errors.Is(err, vector.ErrIndexNotReady) {
			return nil // nothing checkpoint-worthy yet; not an error
		}
		return err
	}

	body, err := encodeCheckpointBody(rec.Seq, rec.IndexType, rec.IndexParams, rec.TrainingGeneration, payload)
	if err != nil {
		return err
	}

	fsys := m.fs()
	nsDir := m.namespaceDir(fsys, namespace)
	if err := fsys.MkdirAll(nsDir, 0o750); err != nil {
		return err
	}

	finalName := fmt.Sprintf("%020d%s", rec.Seq, checkpointSuffix)
	finalPath := fsys.PathJoin(nsDir, finalName)
	tmpPath := finalPath + ".tmp"

	f, err := fsys.Create(tmpPath, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(tmpPath, finalPath); err != nil {
		return err
	}

	entries, err := fsys.List(nsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry == finalName {
			continue
		}
		_ = fsys.Remove(fsys.PathJoin(nsDir, entry))
	}
	return nil
}

// removeCheckpoints deletes every checkpoint file for namespace: called
// when the namespace itself is dropped (docs/vectors/engineering.md
// "Checkpoints", "A drop removes them with the store").
func (m *VectorIndexManager) removeCheckpoints(namespace string) {
	if m.dir == "" {
		return
	}
	fsys := m.fs()
	nsDir := m.namespaceDir(fsys, namespace)
	_ = fsys.RemoveAll(nsDir)
}

// loadCheckpoint tries to load the newest valid checkpoint for namespace
// into live and replay the mutation log from the checkpoint's sequence to
// rec.Seq. It returns true only when live now reflects rec.Seq exactly;
// any failure along the way -- no directory configured, no checkpoint file,
// a corrupt or unreadable one, a format version or geometry mismatch, or a
// replay gap (the log already trimmed past the checkpoint's sequence) --
// returns false so the caller falls back to a full rebuild. A checkpoint is
// an optimisation only: every one of these is a cost, never a correctness
// problem (docs/vectors/engineering.md "Open").
func (m *VectorIndexManager) loadCheckpoint(stg storage.Storage, namespace string, rec vector.NamespaceRecord, live vector.LiveIndex) bool {
	if m.dir == "" {
		return false
	}
	fsys := m.fs()
	nsDir := m.namespaceDir(fsys, namespace)
	entries, err := fsys.List(nsDir)
	if err != nil || len(entries) == 0 {
		return false
	}

	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e, checkpointSuffix) {
			names = append(names, e)
		}
	}
	if len(names) == 0 {
		return false
	}
	sort.Strings(names) // fixed-width zero-padded sequence sorts numerically
	newest := names[len(names)-1]

	f, err := fsys.Open(fsys.PathJoin(nsDir, newest))
	if err != nil {
		return false
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return false
	}

	hdr, err := decodeCheckpointBody(data)
	if err != nil {
		return false
	}
	if hdr.version != checkpointFormatVersion {
		return false
	}
	if hdr.indexType != rec.IndexType || hdr.generation != rec.TrainingGeneration {
		return false // stale geometry: a reconfigure happened since this checkpoint
	}
	if hdr.seq > rec.Seq {
		return false // ahead of the restored record; never trust it
	}
	if err := live.Unmarshal(hdr.payload); err != nil {
		return false
	}
	if hdr.seq == rec.Seq {
		return true
	}
	if err := replayMutationLog(stg, namespace, hdr.seq, rec.Seq, live); err != nil {
		return false
	}
	return true
}

// replayMutationLog applies every mutation-log entry for namespace in
// (fromSeq, toSeq] to live, by ID -- not by replaying each entry's own
// operation, but by reading each touched ID's CURRENT record from stg and
// upserting (or deleting, if the ID no longer exists) its present state.
// This is deliberate and simpler than it looks: the mutation log only needs
// to say WHICH ids changed in the window, because stg already holds the
// final, post-replay-range truth for every one of them regardless of how
// many times a given ID was upserted, updated or deleted across that
// range -- replaying every intermediate state would reach the identical
// result through more work. IDs are processed in ascending order for the
// same cross-replica-determinism reason rebuildIndexFromStore iterates in
// ID order.
//
// Returns an error -- a replay gap, surfaced as a plain Go error from a nil
// Get when a trimmed entry is missing, or any storage fault -- when the log
// cannot answer the window; the caller (loadCheckpoint) treats that as
// "fall back to a full rebuild", never a correctness problem.
func replayMutationLog(stg storage.Storage, namespace string, fromSeq, toSeq uint64, live vector.LiveIndex) error {
	touched := make(map[uint64]struct{})
	for seq := fromSeq + 1; seq <= toSeq; seq++ {
		data, err := stg.Get(vector.MutationLogKey(namespace, seq))
		if err != nil {
			return err
		}
		if data == nil {
			return &vector.InvalidRecordError{Record: "mutation log", Reason: "sequence trimmed out of the retained window"}
		}
		entry, err := vector.DecodeMutationLogEntry(data)
		if err != nil {
			return err
		}
		touched[entry.ID] = struct{}{}
	}

	ids := make([]uint64, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		data, err := stg.Get(vector.VectorKey(namespace, id))
		if err != nil {
			return err
		}
		if data == nil {
			_ = live.Delete(id)
			continue
		}
		rec, err := vector.DecodeVectorRecord(data)
		if err != nil {
			return err
		}
		if err := live.Upsert(id, rec.Embedding); err != nil {
			return err
		}
	}
	return nil
}
