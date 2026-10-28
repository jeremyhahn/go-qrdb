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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// Compile-time interface assertion.
var _ transport.VlogClient = (*Transport)(nil)

// VlogClient implementation: calls service.LogService in-process and answers
// the sdk/go/transport/vlogwire shapes the network transports put on the wire. No
// request names a tenant, so the service confines each to the caller's.

// vlogAdmit admits and authorizes method and returns the log service.
func (t *Transport) vlogAdmit(ctx context.Context, method string, req any) (context.Context, service.LogService, error) {
	ctx, err := t.admit(ctx, method)
	if err != nil {
		return nil, nil, err
	}
	if err := t.authorize(ctx, method); err != nil {
		return nil, nil, err
	}
	if t.closed.Load() {
		return nil, nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, nil, &NilRequestError{Method: method}
	}
	if t.logSvc == nil {
		return nil, nil, &NilServiceError{Service: "LogService"}
	}
	return ctx, t.logSvc, nil
}

// vlogDatabase is the database the transport's requests name.
func (t *Transport) vlogDatabase() string {
	if t.logDatabase == "" {
		return transport.DefaultDatabaseScope
	}
	return t.logDatabase
}

// vlogRef names log within the transport's database.
func (t *Transport) vlogRef(log string) service.LogRef {
	return service.LogRef{Database: t.vlogDatabase(), Log: log}
}

// VlogCreate creates a log.
func (t *Transport) VlogCreate(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogCreate", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.CreateLog(ctx, &service.CreateLogRequest{
		LogRef:            t.vlogRef(req.Log),
		Origin:            req.Origin,
		VerifierKeys:      req.VerifierKeys,
		AllowExpireBodies: req.AllowExpireBodies,
		SigningKeyType:    req.SigningKeyType,
	})
	if err != nil {
		return nil, translateError(err)
	}
	return vlogwire.Create(resp.ID, resp.Key), nil
}

// VlogList lists the logs of the transport's database.
func (t *Transport) VlogList(ctx context.Context, _ *vlogwire.ListRequest) (*vlogwire.ListResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogList", struct{}{})
	if err != nil {
		return nil, err
	}
	resp, err := svc.ListLogs(ctx, &service.ListLogsRequest{Database: t.vlogDatabase()})
	if err != nil {
		return nil, translateError(err)
	}
	return vlogwire.List(resp.Logs), nil
}

// VlogAppend appends entries to a log.
func (t *Transport) VlogAppend(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogAppend", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.Append(ctx, &service.AppendRequest{LogRef: t.vlogRef(req.Log), Entries: req.Entries})
	if err != nil {
		return nil, translateError(err)
	}
	return &vlogwire.AppendResult{FirstIndex: resp.FirstIndex, Count: resp.Count}, nil
}

// VlogGet reads a run of a log's entries.
func (t *Transport) VlogGet(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogGet", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.GetEntries(ctx, &service.GetEntriesRequest{LogRef: t.vlogRef(req.Log), From: req.From, Limit: req.Limit})
	if err != nil {
		return nil, translateError(err)
	}
	return vlogwire.Get(resp.Entries), nil
}

// VlogProveInclusion returns the RFC 9162 inclusion proof of a leaf.
func (t *Transport) VlogProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogProveInclusion", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.ProveInclusion(ctx, &service.InclusionRequest{LogRef: t.vlogRef(req.Log), Index: req.Index, Size: req.Size})
	if err != nil {
		return nil, translateError(err)
	}
	return &vlogwire.InclusionProof{Index: resp.Index, Size: resp.Size, Hashes: resp.Hashes}, nil
}

// VlogProveConsistency returns the RFC 9162 consistency proof between two
// tree sizes.
func (t *Transport) VlogProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogProveConsistency", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.ProveConsistency(ctx, &service.ConsistencyRequest{LogRef: t.vlogRef(req.Log), Size1: req.Size1, Size2: req.Size2})
	if err != nil {
		return nil, translateError(err)
	}
	return &vlogwire.ConsistencyProof{Size1: resp.Size1, Size2: resp.Size2, Hashes: resp.Hashes}, nil
}

// VlogHead returns a log's current head and latest checkpoint.
func (t *Transport) VlogHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogHead", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.Head(ctx, &service.HeadRequest{LogRef: t.vlogRef(req.Log)})
	if err != nil {
		return nil, translateError(err)
	}
	return vlogwire.HeadOf(resp.Head), nil
}

// VlogSignHead signs a log's current head.
func (t *Transport) VlogSignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogSignHead", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.SignHead(ctx, &service.SignHeadRequest{LogRef: t.vlogRef(req.Log)})
	if err != nil {
		return nil, translateError(err)
	}
	return &vlogwire.SignHeadResult{Head: resp.Head, Checkpoint: resp.Checkpoint}, nil
}

// VlogRotateKey rotates a log's head-signing key.
func (t *Transport) VlogRotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogRotateKey", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.RotateKey(ctx, &service.RotateKeyRequest{LogRef: t.vlogRef(req.Log), SigningKeyType: req.SigningKeyType})
	if err != nil {
		return nil, translateError(err)
	}
	return &vlogwire.RotateKeyResult{Key: vlogwire.Key(resp.Key)}, nil
}

// VlogExpireBodies removes the bodies of the entries before a cutoff.
func (t *Transport) VlogExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	ctx, svc, err := t.vlogAdmit(ctx, "VlogExpireBodies", nilIfNil(req))
	if err != nil {
		return nil, err
	}
	resp, err := svc.ExpireBodies(ctx, &service.ExpireBodiesRequest{LogRef: t.vlogRef(req.Log), Cutoff: req.Cutoff})
	if err != nil {
		return nil, translateError(err)
	}
	return &vlogwire.ExpireResult{Expired: resp.Expired}, nil
}

// nilIfNil turns a typed nil request pointer into an untyped nil, so
// vlogAdmit's nil check sees it.
func nilIfNil[T any](req *T) any {
	if req == nil {
		return nil
	}
	return req
}
