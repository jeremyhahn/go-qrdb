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

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// VlogBinding exposes the verifiable (RFC 9162 transparency) logs of the
// active connection's database to the Wails frontend, mirroring the SDK
// VlogClient. Each method takes and returns the vlogwire shapes, so the
// frontend sees the same JSON as the REST /vlog routes.
type VlogBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewVlogBinding creates a new VlogBinding backed by the given connection.
func NewVlogBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *VlogBinding {
	return &VlogBinding{logger: log, conn: conn}
}

// vlogCall runs one VlogClient method on the active connection, logging the
// call and its failure under name.
func vlogCall[Req, Res any](b *VlogBinding, name string, req *Req,
	call func(transport.VlogClient, context.Context, *Req) (*Res, error),
) (*Res, error) {
	b.logger.Debug(name + " called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error(name+" failed", "error", err)
		return nil, err
	}
	res, err := call(cl.Vlog(), context.Background(), req)
	if err != nil {
		b.logger.Error(name+" failed", "error", err)
		return nil, err
	}
	return res, nil
}

// Create creates a log, with a head-signing key when the request names a
// key type.
func (b *VlogBinding) Create(req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	return vlogCall(b, "Create", req, transport.VlogClient.VlogCreate)
}

// List lists the logs of the database.
func (b *VlogBinding) List() (*vlogwire.ListResult, error) {
	return vlogCall(b, "List", &vlogwire.ListRequest{}, transport.VlogClient.VlogList)
}

// Append appends entries to a log.
func (b *VlogBinding) Append(req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	return vlogCall(b, "Append", req, transport.VlogClient.VlogAppend)
}

// Get reads a run of a log's entries.
func (b *VlogBinding) Get(req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	return vlogCall(b, "Get", req, transport.VlogClient.VlogGet)
}

// ProveInclusion returns the RFC 9162 proof that a leaf is in a tree of
// the log.
func (b *VlogBinding) ProveInclusion(req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	return vlogCall(b, "ProveInclusion", req, transport.VlogClient.VlogProveInclusion)
}

// ProveConsistency returns the RFC 9162 proof that one tree of the log is
// a prefix of another.
func (b *VlogBinding) ProveConsistency(req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	return vlogCall(b, "ProveConsistency", req, transport.VlogClient.VlogProveConsistency)
}

// Head returns a log's current tree head and latest checkpoint.
func (b *VlogBinding) Head(req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	return vlogCall(b, "Head", req, transport.VlogClient.VlogHead)
}

// SignHead signs a log's current head, answering its checkpoint as a C2SP
// signed note.
func (b *VlogBinding) SignHead(req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	return vlogCall(b, "SignHead", req, transport.VlogClient.VlogSignHead)
}

// RotateKey rotates a log's head-signing key; a checkpoint signed under an
// earlier key still verifies under it.
func (b *VlogBinding) RotateKey(req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	return vlogCall(b, "RotateKey", req, transport.VlogClient.VlogRotateKey)
}

// ExpireBodies removes the bodies of the entries before a cutoff, on a log
// that allows it.
func (b *VlogBinding) ExpireBodies(req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	return vlogCall(b, "ExpireBodies", req, transport.VlogClient.VlogExpireBodies)
}
