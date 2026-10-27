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

package vlogwire

import "github.com/jeremyhahn/go-qrdb/pkg/vlog"

// The requests below are the JSON a QUIC vlog.* payload, an MCP qrdb_vlog_*
// tool call and a REST /vlog/* write body carry, and the arguments of the
// SDK's VlogClient. Each names a log within the request's database, in the
// caller's own tenant: no request names a tenant.

// LogRequest names a log: the head and sign-head request.
type LogRequest struct {
	Log string `json:"log"`
}

// CreateRequest creates a log. SigningKeyType is empty for a log with no
// head-signing key.
type CreateRequest struct {
	Log               string             `json:"log"`
	Origin            string             `json:"origin,omitempty"`
	VerifierKeys      []string           `json:"verifier_keys,omitempty"`
	AllowExpireBodies bool               `json:"allow_expire_bodies,omitempty"`
	SigningKeyType    vlog.SignatureType `json:"signing_key_type,omitempty"`
}

// ListRequest lists the logs of the request's database.
type ListRequest struct{}

// AppendRequest appends entries to a log.
type AppendRequest struct {
	Log     string   `json:"log"`
	Entries [][]byte `json:"entries"`
}

// GetRequest reads up to Limit entries from index From; a zero Limit asks
// for the service's default.
type GetRequest struct {
	Log   string `json:"log"`
	From  uint64 `json:"from"`
	Limit int    `json:"limit,omitempty"`
}

// InclusionRequest asks for the proof that leaf Index is in the tree of
// Size leaves.
type InclusionRequest struct {
	Log   string `json:"log"`
	Index uint64 `json:"index"`
	Size  uint64 `json:"size"`
}

// ConsistencyRequest asks for the proof that the tree of Size1 leaves is a
// prefix of the tree of Size2.
type ConsistencyRequest struct {
	Log   string `json:"log"`
	Size1 uint64 `json:"size1"`
	Size2 uint64 `json:"size2"`
}

// RotateKeyRequest rotates a log's head-signing key to a new key of
// SigningKeyType.
type RotateKeyRequest struct {
	Log            string             `json:"log"`
	SigningKeyType vlog.SignatureType `json:"signing_key_type"`
}

// ExpireRequest expires the bodies of the entries before index Cutoff.
type ExpireRequest struct {
	Log    string `json:"log"`
	Cutoff uint64 `json:"cutoff"`
}
