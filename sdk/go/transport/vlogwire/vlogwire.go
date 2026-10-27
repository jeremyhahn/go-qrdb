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

// Package vlogwire is the verifiable log's JSON wire: the requests and
// results REST, QUIC, MCP and the SDK carry, and the conversion of pkg/vlog
// values to them. It is the one shape the JSON transports share, so they
// cannot drift apart. It lives in the SDK, as policywire does, because the
// servers and the SDK's clients both import it, and it depends on pkg/vlog
// alone, so a lean SDK embedder compiles no API package. Byte fields (entry bodies, hashes, roots, signatures, checkpoints,
// public keys) encode as base64 under encoding/json's []byte rule. Error
// classification is not here: log errors are rows in pkg/api/errclass, like
// every other condition.
package vlogwire

import "github.com/jeremyhahn/go-qrdb/pkg/vlog"

// HeadKey is a log's head-signing public key. The provider's key reference
// is not part of it: it names the key inside the provider and is no use to
// a verifier.
type HeadKey struct {
	Version   uint32 `json:"version"`
	PublicKey []byte `json:"public_key"`
}

// CreateResult is a created log's identity and its first head-signing key.
type CreateResult struct {
	TenantID string   `json:"tenant_id"`
	Database string   `json:"database"`
	Log      string   `json:"log"`
	Key      *HeadKey `json:"key,omitempty"`
}

// ListResult is the names of a database's logs.
type ListResult struct {
	Logs []string `json:"logs"`
}

// AppendResult is the index of the first entry appended and how many were.
type AppendResult struct {
	FirstIndex uint64 `json:"first_index"`
	Count      int    `json:"count"`
}

// Entry is one log entry. An expired entry carries no body.
type Entry struct {
	Index   uint64 `json:"index"`
	Body    []byte `json:"body,omitempty"`
	Expired bool   `json:"expired,omitempty"`
}

// GetResult is a run of consecutive entries.
type GetResult struct {
	Entries []Entry `json:"entries"`
}

// InclusionProof is an RFC 9162 inclusion proof: the leaf at Index is in
// the tree of Size leaves.
type InclusionProof struct {
	Index  uint64   `json:"index"`
	Size   uint64   `json:"size"`
	Hashes [][]byte `json:"hashes"`
}

// ConsistencyProof is an RFC 9162 consistency proof: the tree of Size1
// leaves is a prefix of the tree of Size2 leaves.
type ConsistencyProof struct {
	Size1  uint64   `json:"size1"`
	Size2  uint64   `json:"size2"`
	Hashes [][]byte `json:"hashes"`
}

// Head is a log's current tree head and its latest checkpoint.
type Head struct {
	Size           uint64 `json:"size"`
	Root           []byte `json:"root"`
	BodiesFrom     uint64 `json:"bodies_from"`
	Checkpoint     []byte `json:"checkpoint,omitempty"`
	CheckpointSize uint64 `json:"checkpoint_size"`
}

// SignHeadResult is a signed tree head, carrying its signature type, and
// its checkpoint as a C2SP signed note.
type SignHeadResult struct {
	Head       vlog.SignedHead `json:"head"`
	Checkpoint []byte          `json:"checkpoint"`
}

// RotateKeyResult is the head-signing key a rotation appended to the log's
// verifier keys, which signs every head from then on.
type RotateKeyResult struct {
	Key HeadKey `json:"key"`
}

// ExpireResult is how many entry bodies an expiry removed.
type ExpireResult struct {
	Expired uint64 `json:"expired"`
}

// Key converts a head-signing key.
func Key(k vlog.HeadKey) HeadKey {
	return HeadKey{Version: k.Version, PublicKey: k.PublicKey}
}

// Create converts a created log's identity and its first head-signing key,
// nil for a log with no head-signing key.
func Create(id vlog.LogID, key *vlog.HeadKey) *CreateResult {
	out := &CreateResult{TenantID: id.TenantID, Database: id.Database, Log: id.Log}
	if key != nil {
		k := Key(*key)
		out.Key = &k
	}
	return out
}

// List converts a database's logs. Logs is never nil, so a database with no
// logs encodes as an empty list rather than null.
func List(ids []vlog.LogID) *ListResult {
	logs := make([]string, len(ids))
	for i, id := range ids {
		logs[i] = id.Log
	}
	return &ListResult{Logs: logs}
}

// Get converts a run of entries. Entries is never nil.
func Get(es []vlog.Entry) *GetResult {
	entries := make([]Entry, len(es))
	for i, e := range es {
		entries[i] = Entry{Index: e.Index, Body: e.Body, Expired: e.Expired}
	}
	return &GetResult{Entries: entries}
}

// HeadOf converts a tree head.
func HeadOf(h vlog.Head) *Head {
	return &Head{Size: h.Size, Root: h.Root, BodiesFrom: h.BodiesFrom, Checkpoint: h.Checkpoint, CheckpointSize: h.CheckpointSize}
}
