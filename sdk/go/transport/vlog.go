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

package transport

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// VlogClient serves the verifiable logs of the transport's database, in the
// caller's own tenant: no request names a tenant. Its methods are named as
// the gRPC ones are, and carry the sdk/go/transport/vlogwire shapes REST, QUIC and
// MCP put on the wire, so every transport answers the same values.
type VlogClient interface {
	// VlogCreate creates a log, with a head-signing key when the request
	// names a key type.
	VlogCreate(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error)

	// VlogList lists the logs of the database.
	VlogList(ctx context.Context, req *vlogwire.ListRequest) (*vlogwire.ListResult, error)

	// VlogAppend appends entries to a log.
	VlogAppend(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error)

	// VlogGet reads a run of a log's entries.
	VlogGet(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error)

	// VlogProveInclusion returns the RFC 9162 proof that a leaf is in a tree
	// of the log.
	VlogProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error)

	// VlogProveConsistency returns the RFC 9162 proof that one tree of the
	// log is a prefix of another.
	VlogProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error)

	// VlogHead returns a log's current tree head and latest checkpoint.
	VlogHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error)

	// VlogSignHead signs a log's current head, answering its checkpoint as
	// a C2SP signed note.
	VlogSignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error)

	// VlogRotateKey rotates a log's head-signing key; a checkpoint signed
	// under an earlier key still verifies under it.
	VlogRotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error)

	// VlogExpireBodies removes the bodies of the entries before a cutoff, on
	// a log that allows it.
	VlogExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error)
}
