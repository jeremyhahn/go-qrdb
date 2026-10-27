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

package grpc

import (
	"context"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// vlogSignatureTypeFromProto maps the wire signature type to the vlog one.
// UNSPECIFIED is the empty type: a log created with it has no owner key.
var vlogSignatureTypeFromProto = map[pb.VlogSignatureType]vlog.SignatureType{
	pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_UNSPECIFIED:       "",
	pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519:           vlog.SignatureEd25519,
	pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256: vlog.SignatureECDSAP256SHA256,
}

// vlogSignatureTypeToProto maps a vlog signature type to the wire one.
var vlogSignatureTypeToProto = map[vlog.SignatureType]pb.VlogSignatureType{
	vlog.SignatureEd25519:         pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519,
	vlog.SignatureECDSAP256SHA256: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ECDSA_P256_SHA256,
}

// logService returns the configured LogService, or a ServiceNotConfiguredError
// when the node serves no verifiable logs.
func (s *qrdbServiceServer) logService() (service.LogService, error) {
	if s.logSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "vlog"}
	}
	return s.logSvc, nil
}

// vlogRef names a log in the caller's tenant and the request's database. The
// tenant is left empty so the service confines the request to the caller's.
func vlogRef(ctx context.Context, log string) service.LogRef {
	return service.LogRef{Database: databaseFromContext(ctx), Log: log}
}

// vlogLogIDToProto converts a vlog.LogID to its wire form.
func vlogLogIDToProto(id vlog.LogID) *pb.VlogLogID {
	return &pb.VlogLogID{TenantId: id.TenantID, Database: id.Database, Log: id.Log}
}

// VlogCreate creates a verifiable log.
func (s *qrdbServiceServer) VlogCreate(ctx context.Context, req *pb.VlogCreateRequest) (*pb.VlogCreateResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	keyType, ok := vlogSignatureTypeFromProto[req.GetSigningKeyType()]
	if !ok {
		return nil, mapServiceError(&vlog.UnsupportedKeyTypeError{Type: req.GetSigningKeyType().String()})
	}
	resp, err := logSvc.CreateLog(ctx, &service.CreateLogRequest{
		LogRef:            vlogRef(ctx, req.GetLog()),
		Origin:            req.GetOrigin(),
		VerifierKeys:      req.GetVerifierKeys(),
		AllowExpireBodies: req.GetAllowExpireBodies(),
		SigningKeyType:    keyType,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	out := &pb.VlogCreateResponse{Log: vlogLogIDToProto(resp.ID)}
	if resp.Key != nil {
		out.Key = vlogHeadKeyToProto(*resp.Key)
	}
	return out, nil
}

// vlogHeadKeyToProto converts a vlog.HeadKey to its wire form.
func vlogHeadKeyToProto(k vlog.HeadKey) *pb.VlogHeadKey {
	return &pb.VlogHeadKey{Version: k.Version, Ref: k.Ref, PublicKey: k.PublicKey}
}

// VlogList lists the verifiable logs in the caller's tenant and database.
func (s *qrdbServiceServer) VlogList(ctx context.Context, _ *pb.VlogListRequest) (*pb.VlogListResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.ListLogs(ctx, &service.ListLogsRequest{Database: databaseFromContext(ctx)})
	if err != nil {
		return nil, mapServiceError(err)
	}
	logs := make([]*pb.VlogLogID, len(resp.Logs))
	for i, id := range resp.Logs {
		logs[i] = vlogLogIDToProto(id)
	}
	return &pb.VlogListResponse{Logs: logs}, nil
}

// VlogAppend appends entries to a verifiable log.
func (s *qrdbServiceServer) VlogAppend(ctx context.Context, req *pb.VlogAppendRequest) (*pb.VlogAppendResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.Append(ctx, &service.AppendRequest{
		LogRef:  vlogRef(ctx, req.GetLog()),
		Entries: req.GetEntries(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VlogAppendResponse{FirstIndex: resp.FirstIndex, Count: int64(resp.Count)}, nil
}

// VlogGet reads a range of a verifiable log's entries.
func (s *qrdbServiceServer) VlogGet(ctx context.Context, req *pb.VlogGetRequest) (*pb.VlogGetResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.GetEntries(ctx, &service.GetEntriesRequest{
		LogRef: vlogRef(ctx, req.GetLog()),
		From:   req.GetFrom(),
		Limit:  int(req.GetLimit()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	entries := make([]*pb.VlogEntry, len(resp.Entries))
	for i, e := range resp.Entries {
		entries[i] = &pb.VlogEntry{Index: e.Index, Body: e.Body, Expired: e.Expired}
	}
	return &pb.VlogGetResponse{Entries: entries}, nil
}

// VlogProveInclusion returns an inclusion proof for one leaf.
func (s *qrdbServiceServer) VlogProveInclusion(ctx context.Context, req *pb.VlogProveInclusionRequest) (*pb.VlogInclusionProof, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.ProveInclusion(ctx, &service.InclusionRequest{
		LogRef: vlogRef(ctx, req.GetLog()),
		Index:  req.GetLeafIndex(),
		Size:   req.GetTreeSize(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VlogInclusionProof{LeafIndex: resp.Index, TreeSize: resp.Size, Hashes: resp.Hashes}, nil
}

// VlogProveConsistency returns a consistency proof between two tree sizes.
func (s *qrdbServiceServer) VlogProveConsistency(ctx context.Context, req *pb.VlogProveConsistencyRequest) (*pb.VlogConsistencyProof, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.ProveConsistency(ctx, &service.ConsistencyRequest{
		LogRef: vlogRef(ctx, req.GetLog()),
		Size1:  req.GetSize1(),
		Size2:  req.GetSize2(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VlogConsistencyProof{Size1: resp.Size1, Size2: resp.Size2, Hashes: resp.Hashes}, nil
}

// VlogHead returns a verifiable log's head.
func (s *qrdbServiceServer) VlogHead(ctx context.Context, req *pb.VlogHeadRequest) (*pb.VlogHeadResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.Head(ctx, &service.HeadRequest{LogRef: vlogRef(ctx, req.GetLog())})
	if err != nil {
		return nil, mapServiceError(err)
	}
	h := resp.Head
	return &pb.VlogHeadResponse{
		Size:           h.Size,
		Root:           h.Root,
		BodiesFrom:     h.BodiesFrom,
		Checkpoint:     h.Checkpoint,
		CheckpointSize: h.CheckpointSize,
	}, nil
}

// VlogSignHead signs a verifiable log's current head.
func (s *qrdbServiceServer) VlogSignHead(ctx context.Context, req *pb.VlogSignHeadRequest) (*pb.VlogSignHeadResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.SignHead(ctx, &service.SignHeadRequest{LogRef: vlogRef(ctx, req.GetLog())})
	if err != nil {
		return nil, mapServiceError(err)
	}
	h := resp.Head
	sigType, ok := vlogSignatureTypeToProto[h.SignatureType]
	if !ok {
		return nil, mapServiceError(&vlog.UnsupportedKeyTypeError{Type: string(h.SignatureType)})
	}
	return &pb.VlogSignHeadResponse{
		Head: &pb.VlogSignedHead{
			Size:          h.Size,
			Root:          h.Root,
			KeyVersion:    h.KeyVersion,
			SignatureType: sigType,
			Signature:     h.Signature,
		},
		Checkpoint: resp.Checkpoint,
	}, nil
}

// VlogExpireBodies drops the entry bodies below a cutoff.
func (s *qrdbServiceServer) VlogExpireBodies(ctx context.Context, req *pb.VlogExpireBodiesRequest) (*pb.VlogExpireBodiesResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	resp, err := logSvc.ExpireBodies(ctx, &service.ExpireBodiesRequest{
		LogRef: vlogRef(ctx, req.GetLog()),
		Cutoff: req.GetCutoff(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VlogExpireBodiesResponse{Expired: resp.Expired}, nil
}

// VlogRotateKey makes a new key the one that signs a verifiable log's heads.
func (s *qrdbServiceServer) VlogRotateKey(ctx context.Context, req *pb.VlogRotateKeyRequest) (*pb.VlogRotateKeyResponse, error) {
	logSvc, err := s.logService()
	if err != nil {
		return nil, mapServiceError(err)
	}
	// UNSPECIFIED is the empty type, which creates no key: a rotation names one.
	keyType, ok := vlogSignatureTypeFromProto[req.GetSigningKeyType()]
	if !ok || keyType == "" {
		return nil, mapServiceError(&vlog.UnsupportedKeyTypeError{Type: req.GetSigningKeyType().String()})
	}
	resp, err := logSvc.RotateKey(ctx, &service.RotateKeyRequest{
		LogRef:         vlogRef(ctx, req.GetLog()),
		SigningKeyType: keyType,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.VlogRotateKeyResponse{Key: vlogHeadKeyToProto(resp.Key)}, nil
}
