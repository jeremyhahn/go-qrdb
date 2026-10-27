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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// The vlog.* frames carry JSON, the way the audit.* frames do: the request
// payloads below, and the results of sdk/go/transport/vlogwire. Byte fields are
// base64 strings under encoding/json's []byte rule. Every request names
// the log within the frame's database (tenant.DatabaseFromContext), in the
// caller's own tenant: like every transport, the payload names no tenant.

// vlogRef names log within the frame's database, in the caller's tenant.
func vlogRef(ctx context.Context, log string) service.LogRef {
	return service.LogRef{Database: tenant.DatabaseFromContext(ctx), Log: log}
}

// vlogFrame decodes payload into a Req, calls the log service and encodes
// the result. It is the one body every vlog.* handler shares.
func vlogFrame[Req, Res any](s *Server, payload []byte, call func(service.LogService, Req) (Res, error)) ([]byte, error) {
	if s.logSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "vlog"}
	}
	var req Req
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	res, err := call(s.logSvc, req)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return b, nil
}

func (s *Server) handleVlogCreate(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
		resp, err := svc.CreateLog(ctx, &service.CreateLogRequest{
			LogRef:            vlogRef(ctx, p.Log),
			Origin:            p.Origin,
			VerifierKeys:      p.VerifierKeys,
			AllowExpireBodies: p.AllowExpireBodies,
			SigningKeyType:    p.SigningKeyType,
		})
		if err != nil {
			return nil, err
		}
		return vlogwire.Create(resp.ID, resp.Key), nil
	})
}

func (s *Server) handleVlogList(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, _ vlogwire.ListRequest) (*vlogwire.ListResult, error) {
		resp, err := svc.ListLogs(ctx, &service.ListLogsRequest{Database: tenant.DatabaseFromContext(ctx)})
		if err != nil {
			return nil, err
		}
		return vlogwire.List(resp.Logs), nil
	})
}

func (s *Server) handleVlogAppend(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
		resp, err := svc.Append(ctx, &service.AppendRequest{LogRef: vlogRef(ctx, p.Log), Entries: p.Entries})
		if err != nil {
			return nil, err
		}
		return &vlogwire.AppendResult{FirstIndex: resp.FirstIndex, Count: resp.Count}, nil
	})
}

func (s *Server) handleVlogGet(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.GetRequest) (*vlogwire.GetResult, error) {
		resp, err := svc.GetEntries(ctx, &service.GetEntriesRequest{LogRef: vlogRef(ctx, p.Log), From: p.From, Limit: p.Limit})
		if err != nil {
			return nil, err
		}
		return vlogwire.Get(resp.Entries), nil
	})
}

func (s *Server) handleVlogProveInclusion(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
		resp, err := svc.ProveInclusion(ctx, &service.InclusionRequest{LogRef: vlogRef(ctx, p.Log), Index: p.Index, Size: p.Size})
		if err != nil {
			return nil, err
		}
		return &vlogwire.InclusionProof{Index: resp.Index, Size: resp.Size, Hashes: resp.Hashes}, nil
	})
}

func (s *Server) handleVlogProveConsistency(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
		resp, err := svc.ProveConsistency(ctx, &service.ConsistencyRequest{LogRef: vlogRef(ctx, p.Log), Size1: p.Size1, Size2: p.Size2})
		if err != nil {
			return nil, err
		}
		return &vlogwire.ConsistencyProof{Size1: resp.Size1, Size2: resp.Size2, Hashes: resp.Hashes}, nil
	})
}

func (s *Server) handleVlogHead(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.LogRequest) (*vlogwire.Head, error) {
		resp, err := svc.Head(ctx, &service.HeadRequest{LogRef: vlogRef(ctx, p.Log)})
		if err != nil {
			return nil, err
		}
		return vlogwire.HeadOf(resp.Head), nil
	})
}

func (s *Server) handleVlogSignHead(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
		resp, err := svc.SignHead(ctx, &service.SignHeadRequest{LogRef: vlogRef(ctx, p.Log)})
		if err != nil {
			return nil, err
		}
		return &vlogwire.SignHeadResult{Head: resp.Head, Checkpoint: resp.Checkpoint}, nil
	})
}

func (s *Server) handleVlogRotateKey(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
		resp, err := svc.RotateKey(ctx, &service.RotateKeyRequest{LogRef: vlogRef(ctx, p.Log), SigningKeyType: p.SigningKeyType})
		if err != nil {
			return nil, err
		}
		return &vlogwire.RotateKeyResult{Key: vlogwire.Key(resp.Key)}, nil
	})
}

func (s *Server) handleVlogExpireBodies(ctx context.Context, payload []byte) ([]byte, error) {
	return vlogFrame(s, payload, func(svc service.LogService, p vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
		resp, err := svc.ExpireBodies(ctx, &service.ExpireBodiesRequest{LogRef: vlogRef(ctx, p.Log), Cutoff: p.Cutoff})
		if err != nil {
			return nil, err
		}
		return &vlogwire.ExpireResult{Expired: resp.Expired}, nil
	})
}
