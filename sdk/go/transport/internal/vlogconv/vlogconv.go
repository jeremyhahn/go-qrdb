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

// Package vlogconv issues the Vlog* RPCs over a QRDBService client and
// converts between the protobuf messages and the sdk/go/transport/vlogwire shapes the
// SDK's VlogClient carries. The gRPC and Unix transports drive the same
// protobuf client, so the conversion lives here once. It is internal: only
// the transport implementations may import it.
package vlogconv

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// signatureTypeToProto maps a head-signing key type onto its protobuf enum.
// The empty type is a log with no key.
var signatureTypeToProto = map[vlog.SignatureType]pb.VlogSignatureType{
	"":                            pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_UNSPECIFIED,
	vlog.SignatureEd25519:         pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519,
	vlog.SignatureECDSAP256SHA256: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256,
}

// signatureTypeFromProto maps a signed head's protobuf signature type back.
// UNSPECIFIED is absent: every signed head names its type.
var signatureTypeFromProto = map[pb.VlogSignatureType]vlog.SignatureType{
	pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519:           vlog.SignatureEd25519,
	pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256: vlog.SignatureECDSAP256SHA256,
}

// Caller issues the Vlog* RPCs over a QRDBService client and converts the
// results. Each transport supplies its own error mapping; the closed-transport
// check stays with the transport.
type Caller struct {
	Client   pb.QRDBServiceClient
	MapError func(method string, err error) error
}

// Create issues VlogCreate.
func (c Caller) Create(ctx context.Context, req *vlogwire.CreateRequest) (*vlogwire.CreateResult, error) {
	if req == nil {
		return nil, nilRequest("VlogCreate")
	}
	keyType, err := toProto("VlogCreate", req.SigningKeyType)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.VlogCreate(ctx, &pb.VlogCreateRequest{
		Log:               req.Log,
		Origin:            req.Origin,
		VerifierKeys:      req.VerifierKeys,
		AllowExpireBodies: req.AllowExpireBodies,
		SigningKeyType:    keyType,
	})
	if err != nil {
		return nil, c.MapError("VlogCreate", err)
	}
	id := resp.GetLog()
	out := &vlogwire.CreateResult{TenantID: id.GetTenantId(), Database: id.GetDatabase(), Log: id.GetLog()}
	if k := resp.GetKey(); k != nil {
		key := headKey(k)
		out.Key = &key
	}
	return out, nil
}

// List issues VlogList.
func (c Caller) List(ctx context.Context, _ *vlogwire.ListRequest) (*vlogwire.ListResult, error) {
	resp, err := c.Client.VlogList(ctx, &pb.VlogListRequest{})
	if err != nil {
		return nil, c.MapError("VlogList", err)
	}
	out := &vlogwire.ListResult{Logs: make([]string, 0, len(resp.GetLogs()))}
	for _, id := range resp.GetLogs() {
		out.Logs = append(out.Logs, id.GetLog())
	}
	return out, nil
}

// Append issues VlogAppend.
func (c Caller) Append(ctx context.Context, req *vlogwire.AppendRequest) (*vlogwire.AppendResult, error) {
	if req == nil {
		return nil, nilRequest("VlogAppend")
	}
	resp, err := c.Client.VlogAppend(ctx, &pb.VlogAppendRequest{Log: req.Log, Entries: req.Entries})
	if err != nil {
		return nil, c.MapError("VlogAppend", err)
	}
	return &vlogwire.AppendResult{FirstIndex: resp.GetFirstIndex(), Count: int(resp.GetCount())}, nil
}

// Get issues VlogGet.
func (c Caller) Get(ctx context.Context, req *vlogwire.GetRequest) (*vlogwire.GetResult, error) {
	if req == nil {
		return nil, nilRequest("VlogGet")
	}
	resp, err := c.Client.VlogGet(ctx, &pb.VlogGetRequest{Log: req.Log, From: req.From, Limit: int64(req.Limit)})
	if err != nil {
		return nil, c.MapError("VlogGet", err)
	}
	out := &vlogwire.GetResult{Entries: make([]vlogwire.Entry, 0, len(resp.GetEntries()))}
	for _, e := range resp.GetEntries() {
		out.Entries = append(out.Entries, vlogwire.Entry{Index: e.GetIndex(), Body: e.GetBody(), Expired: e.GetExpired()})
	}
	return out, nil
}

// ProveInclusion issues VlogProveInclusion.
func (c Caller) ProveInclusion(ctx context.Context, req *vlogwire.InclusionRequest) (*vlogwire.InclusionProof, error) {
	if req == nil {
		return nil, nilRequest("VlogProveInclusion")
	}
	resp, err := c.Client.VlogProveInclusion(ctx, &pb.VlogProveInclusionRequest{Log: req.Log, LeafIndex: req.Index, TreeSize: req.Size})
	if err != nil {
		return nil, c.MapError("VlogProveInclusion", err)
	}
	return &vlogwire.InclusionProof{Index: resp.GetLeafIndex(), Size: resp.GetTreeSize(), Hashes: resp.GetHashes()}, nil
}

// ProveConsistency issues VlogProveConsistency.
func (c Caller) ProveConsistency(ctx context.Context, req *vlogwire.ConsistencyRequest) (*vlogwire.ConsistencyProof, error) {
	if req == nil {
		return nil, nilRequest("VlogProveConsistency")
	}
	resp, err := c.Client.VlogProveConsistency(ctx, &pb.VlogProveConsistencyRequest{Log: req.Log, Size1: req.Size1, Size2: req.Size2})
	if err != nil {
		return nil, c.MapError("VlogProveConsistency", err)
	}
	return &vlogwire.ConsistencyProof{Size1: resp.GetSize1(), Size2: resp.GetSize2(), Hashes: resp.GetHashes()}, nil
}

// Head issues VlogHead.
func (c Caller) Head(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.Head, error) {
	if req == nil {
		return nil, nilRequest("VlogHead")
	}
	resp, err := c.Client.VlogHead(ctx, &pb.VlogHeadRequest{Log: req.Log})
	if err != nil {
		return nil, c.MapError("VlogHead", err)
	}
	return &vlogwire.Head{
		Size:           resp.GetSize(),
		Root:           resp.GetRoot(),
		BodiesFrom:     resp.GetBodiesFrom(),
		Checkpoint:     resp.GetCheckpoint(),
		CheckpointSize: resp.GetCheckpointSize(),
	}, nil
}

// SignHead issues VlogSignHead.
func (c Caller) SignHead(ctx context.Context, req *vlogwire.LogRequest) (*vlogwire.SignHeadResult, error) {
	if req == nil {
		return nil, nilRequest("VlogSignHead")
	}
	resp, err := c.Client.VlogSignHead(ctx, &pb.VlogSignHeadRequest{Log: req.Log})
	if err != nil {
		return nil, c.MapError("VlogSignHead", err)
	}
	h := resp.GetHead()
	sigType, ok := signatureTypeFromProto[h.GetSignatureType()]
	if !ok {
		return nil, &transport.RequestError{Method: "VlogSignHead", Err: &vlog.UnsupportedKeyTypeError{Type: h.GetSignatureType().String()}}
	}
	return &vlogwire.SignHeadResult{
		Head: vlog.SignedHead{
			Size:          h.GetSize(),
			Root:          h.GetRoot(),
			KeyVersion:    h.GetKeyVersion(),
			SignatureType: sigType,
			Signature:     h.GetSignature(),
		},
		Checkpoint: resp.GetCheckpoint(),
	}, nil
}

// RotateKey issues VlogRotateKey.
func (c Caller) RotateKey(ctx context.Context, req *vlogwire.RotateKeyRequest) (*vlogwire.RotateKeyResult, error) {
	if req == nil {
		return nil, nilRequest("VlogRotateKey")
	}
	keyType, err := toProto("VlogRotateKey", req.SigningKeyType)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.VlogRotateKey(ctx, &pb.VlogRotateKeyRequest{Log: req.Log, SigningKeyType: keyType})
	if err != nil {
		return nil, c.MapError("VlogRotateKey", err)
	}
	return &vlogwire.RotateKeyResult{Key: headKey(resp.GetKey())}, nil
}

// ExpireBodies issues VlogExpireBodies.
func (c Caller) ExpireBodies(ctx context.Context, req *vlogwire.ExpireRequest) (*vlogwire.ExpireResult, error) {
	if req == nil {
		return nil, nilRequest("VlogExpireBodies")
	}
	resp, err := c.Client.VlogExpireBodies(ctx, &pb.VlogExpireBodiesRequest{Log: req.Log, Cutoff: req.Cutoff})
	if err != nil {
		return nil, c.MapError("VlogExpireBodies", err)
	}
	return &vlogwire.ExpireResult{Expired: resp.GetExpired()}, nil
}

// headKey converts a protobuf head key; the key-store reference stays
// server-side, as on every other transport.
func headKey(k *pb.VlogHeadKey) vlogwire.HeadKey {
	return vlogwire.HeadKey{Version: k.GetVersion(), PublicKey: k.GetPublicKey()}
}

// toProto maps a requested key type onto its enum, refusing one the protocol
// cannot carry before any RPC is sent.
func toProto(method string, t vlog.SignatureType) (pb.VlogSignatureType, error) {
	v, ok := signatureTypeToProto[t]
	if !ok {
		return 0, &transport.RequestError{Method: method, Err: &vlog.UnsupportedKeyTypeError{Type: string(t)}}
	}
	return v, nil
}

func nilRequest(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}
