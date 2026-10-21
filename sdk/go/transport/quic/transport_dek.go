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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/dekconv"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// DEKClient implementation: the dek.* QUIC methods, framed with the gRPC
// protobuf messages. The server handlers live in pkg/api/quic/handlers_dek.go.

// TenantDEKStatus returns the caller's keyring versions.
func (t *Transport) TenantDEKStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, dekNilRequest("TenantDEKStatus")
	}
	var resp pb.TenantDEKStatusResponse
	if err := t.protoCall(ctx, "dek.Status", &pb.TenantDEKRequest{TenantId: req.TenantID}, &resp); err != nil {
		return nil, err
	}
	return dekconv.Status(&resp), nil
}

// TenantDEKRotate adds a key version and makes it current.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, dekNilRequest("TenantDEKRotate")
	}
	var resp pb.TenantDEKStatusResponse
	if err := t.protoCall(ctx, "dek.Rotate", &pb.TenantDEKRequest{TenantId: req.TenantID}, &resp); err != nil {
		return nil, err
	}
	return dekconv.Status(&resp), nil
}

// TenantDEKRetire destroys an old key version.
func (t *Transport) TenantDEKRetire(ctx context.Context, req *transport.TenantDEKRetireRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, dekNilRequest("TenantDEKRetire")
	}
	var resp pb.TenantDEKStatusResponse
	if err := t.protoCall(ctx, "dek.Retire", &pb.TenantDEKRetireRequest{TenantId: req.TenantID, Version: req.Version}, &resp); err != nil {
		return nil, err
	}
	return dekconv.Status(&resp), nil
}

// TenantDEKEncrypt seals plaintext under the current key version.
func (t *Transport) TenantDEKEncrypt(ctx context.Context, req *transport.TenantDEKEncryptRequest) (*transport.TenantDEKEncryptResponse, error) {
	if req == nil {
		return nil, dekNilRequest("TenantDEKEncrypt")
	}
	var resp pb.TenantDEKEncryptResponse
	in := &pb.TenantDEKEncryptRequest{TenantId: req.TenantID, Plaintext: req.Plaintext, Aad: req.AAD}
	if err := t.protoCall(ctx, "dek.Encrypt", in, &resp); err != nil {
		return nil, err
	}
	return dekconv.Encrypt(&resp), nil
}

// TenantDEKDecrypt opens a ciphertext sealed for the caller's tenant.
func (t *Transport) TenantDEKDecrypt(ctx context.Context, req *transport.TenantDEKDecryptRequest) (*transport.TenantDEKDecryptResponse, error) {
	if req == nil {
		return nil, dekNilRequest("TenantDEKDecrypt")
	}
	var resp pb.TenantDEKDecryptResponse
	in := &pb.TenantDEKDecryptRequest{TenantId: req.TenantID, Ciphertext: req.Ciphertext, Aad: req.AAD}
	if err := t.protoCall(ctx, "dek.Decrypt", in, &resp); err != nil {
		return nil, err
	}
	return dekconv.Decrypt(&resp), nil
}

// TenantDEKRewrap re-seals a ciphertext under the current key version.
func (t *Transport) TenantDEKRewrap(ctx context.Context, req *transport.TenantDEKRewrapRequest) (*transport.TenantDEKRewrapResponse, error) {
	if req == nil {
		return nil, dekNilRequest("TenantDEKRewrap")
	}
	var resp pb.TenantDEKRewrapResponse
	in := &pb.TenantDEKRewrapRequest{TenantId: req.TenantID, Ciphertext: req.Ciphertext, Aad: req.AAD}
	if err := t.protoCall(ctx, "dek.Rewrap", in, &resp); err != nil {
		return nil, err
	}
	return dekconv.Rewrap(&resp), nil
}

// protoCall frames in, invokes method and decodes the reply into out.
func (t *Transport) protoCall(ctx context.Context, method string, in, out proto.Message) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(in)
	if err != nil {
		return &marshalError{Err: err}
	}
	reply, err := t.call(ctx, method, payload)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(reply, out); err != nil {
		return &unmarshalError{Err: err}
	}
	return nil
}

// dekNilRequest is the error for a nil request struct.
func dekNilRequest(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}
