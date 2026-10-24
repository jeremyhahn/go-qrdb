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

package vlog

import "github.com/jeremyhahn/go-qrdb/pkg/storage"

// Request is a read of a named log a state machine answers in its Lookup,
// beside audit.QueryRequest and audit.VerifyRequest. Every request is
// answered from committed storage alone, so every replica answers it alike.
// The set is closed: HeadRequest, GetRequest, InclusionRequest,
// ConsistencyRequest, DescriptorRequest and KeyRequest.
type Request interface {
	read(r storage.Reader) (any, error)
}

// HeadRequest reads a log's head; Read answers a *Head.
type HeadRequest struct {
	ID LogID
}

// GetRequest reads up to Limit entries from index From; Read answers an
// []Entry.
type GetRequest struct {
	ID    LogID
	From  uint64
	Limit int
}

// InclusionRequest reads the inclusion proof of the entry at Index in the
// tree of Size entries; Read answers the proof's hashes as [][]byte.
type InclusionRequest struct {
	ID    LogID
	Index uint64
	Size  uint64
}

// ConsistencyRequest reads the consistency proof between the trees of Size1
// and Size2 entries; Read answers the proof's hashes as [][]byte.
type ConsistencyRequest struct {
	ID    LogID
	Size1 uint64
	Size2 uint64
}

// Read answers req from r, exactly as the storage read it names: ReadHead,
// Get, Inclusion or Consistency.
func Read(r storage.Reader, req Request) (any, error) {
	return req.read(r)
}

// Snapshotter is storage that can open a point-in-time snapshot of itself;
// storage.Storage is one.
type Snapshotter interface {
	Snapshot() (storage.Snapshot, error)
}

// ReadSnapshot answers req, as Read does, from one point-in-time snapshot of
// s that it closes before returning. It is how a state machine answers a log
// read: a read is several storage reads -- the head row, then entry bodies,
// Merkle nodes or checkpoints -- and a state machine's reads may run beside
// its updates (a standalone node's local backend, a Raft disk state
// machine's Lookup). Over live storage one read could then combine two
// commits: Get a low-watermark from before an ExpireBodies with the bodies
// after it, reporting a CorruptTableError, or ReadHead a root from before a
// Checkpoint with the checkpoint after it. A snapshot holds every storage
// read of the request to one commit.
func ReadSnapshot(s Snapshotter, req Request) (any, error) {
	snap, err := s.Snapshot()
	if err != nil {
		return nil, &TableReadError{Err: err}
	}
	v, err := Read(snap, req)
	if cerr := snap.Close(); err == nil && cerr != nil {
		return nil, &TableReadError{Err: cerr}
	}
	return v, err
}

func (q HeadRequest) read(r storage.Reader) (any, error) {
	return ReadHead(r, q.ID)
}

func (q GetRequest) read(r storage.Reader) (any, error) {
	return Get(r, q.ID, q.From, q.Limit)
}

func (q InclusionRequest) read(r storage.Reader) (any, error) {
	return Inclusion(r, q.ID, q.Index, q.Size)
}

func (q ConsistencyRequest) read(r storage.Reader) (any, error) {
	return Consistency(r, q.ID, q.Size1, q.Size2)
}

// DescriptorRequest reads a log's descriptor; Read answers a *Descriptor.
type DescriptorRequest struct {
	ID LogID
}

func (q DescriptorRequest) read(r storage.Reader) (any, error) {
	return ReadDescriptor(r, q.ID)
}

// KeyRequest reads a log's head-signing key at Version; Read answers a
// *SigningKey. Only the software HeadSignerProvider asks it: the key never
// leaves the server.
type KeyRequest struct {
	ID      LogID
	Version uint32
}

func (q KeyRequest) read(r storage.Reader) (any, error) {
	return ReadSigningKey(r, q.ID, q.Version)
}
