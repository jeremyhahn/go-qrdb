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

// Package dekconv converts the protobuf tenant DEK keyring messages into the
// transport-neutral types, and issues the keyring RPCs over a QRDBService
// client. The gRPC, Unix and QUIC transports exchange the same protobuf
// messages, so both live here once. It is internal: only the transport
// implementations may import it.
package dekconv

import (
	"context"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Status converts a protobuf keyring status. Timestamps travel as Unix
// seconds; a zero retired_at_unix means the version is live.
func Status(resp *pb.TenantDEKStatusResponse) *transport.TenantDEKStatusResponse {
	out := &transport.TenantDEKStatusResponse{
		TenantID:       resp.GetTenantId(),
		Algorithm:      resp.GetAlgorithm(),
		CurrentVersion: resp.GetCurrentVersion(),
		Versions:       make([]transport.TenantDEKVersion, 0, len(resp.GetVersions())),
	}
	for _, v := range resp.GetVersions() {
		tv := transport.TenantDEKVersion{
			Version:    v.GetVersion(),
			CreatedAt:  time.Unix(v.GetCreatedAtUnix(), 0).UTC(),
			ProviderID: v.GetProviderId(),
			KeyRef:     v.GetKeyRef(),
		}
		if v.GetRetiredAtUnix() != 0 {
			at := time.Unix(v.GetRetiredAtUnix(), 0).UTC()
			tv.RetiredAt = &at
		}
		out.Versions = append(out.Versions, tv)
	}
	return out
}

// Encrypt converts a protobuf encrypt response.
func Encrypt(resp *pb.TenantDEKEncryptResponse) *transport.TenantDEKEncryptResponse {
	return &transport.TenantDEKEncryptResponse{Ciphertext: resp.GetCiphertext(), Version: resp.GetVersion()}
}

// Decrypt converts a protobuf decrypt response.
func Decrypt(resp *pb.TenantDEKDecryptResponse) *transport.TenantDEKDecryptResponse {
	return &transport.TenantDEKDecryptResponse{Plaintext: resp.GetPlaintext(), Version: resp.GetVersion()}
}

// Rewrap converts a protobuf rewrap response.
func Rewrap(resp *pb.TenantDEKRewrapResponse) *transport.TenantDEKRewrapResponse {
	return &transport.TenantDEKRewrapResponse{
		Ciphertext:  resp.GetCiphertext(),
		FromVersion: resp.GetFromVersion(),
		ToVersion:   resp.GetToVersion(),
	}
}

// Caller issues the TenantDEK* RPCs over a QRDBService client and converts
// the results. The gRPC and Unix transports drive the same protobuf client,
// so they share this rather than each carrying a copy; each supplies its own
// closed-transport check and error mapping.
type Caller struct {
	Client   pb.QRDBServiceClient
	MapError func(method string, err error) error
}

// Status issues TenantDEKStatus.
func (c Caller) Status(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, nilRequest("TenantDEKStatus")
	}
	resp, err := c.Client.TenantDEKStatus(ctx, &pb.TenantDEKRequest{TenantId: req.TenantID})
	if err != nil {
		return nil, c.MapError("TenantDEKStatus", err)
	}
	return Status(resp), nil
}

// Rotate issues TenantDEKRotate.
func (c Caller) Rotate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, nilRequest("TenantDEKRotate")
	}
	resp, err := c.Client.TenantDEKRotate(ctx, &pb.TenantDEKRequest{TenantId: req.TenantID})
	if err != nil {
		return nil, c.MapError("TenantDEKRotate", err)
	}
	return Status(resp), nil
}

// Retire issues TenantDEKRetire.
func (c Caller) Retire(ctx context.Context, req *transport.TenantDEKRetireRequest) (*transport.TenantDEKStatusResponse, error) {
	if req == nil {
		return nil, nilRequest("TenantDEKRetire")
	}
	resp, err := c.Client.TenantDEKRetire(ctx, &pb.TenantDEKRetireRequest{TenantId: req.TenantID, Version: req.Version})
	if err != nil {
		return nil, c.MapError("TenantDEKRetire", err)
	}
	return Status(resp), nil
}

// Encrypt issues TenantDEKEncrypt.
func (c Caller) Encrypt(ctx context.Context, req *transport.TenantDEKEncryptRequest) (*transport.TenantDEKEncryptResponse, error) {
	if req == nil {
		return nil, nilRequest("TenantDEKEncrypt")
	}
	resp, err := c.Client.TenantDEKEncrypt(ctx, &pb.TenantDEKEncryptRequest{
		TenantId:  req.TenantID,
		Plaintext: req.Plaintext,
		Aad:       req.AAD,
	})
	if err != nil {
		return nil, c.MapError("TenantDEKEncrypt", err)
	}
	return Encrypt(resp), nil
}

// Decrypt issues TenantDEKDecrypt.
func (c Caller) Decrypt(ctx context.Context, req *transport.TenantDEKDecryptRequest) (*transport.TenantDEKDecryptResponse, error) {
	if req == nil {
		return nil, nilRequest("TenantDEKDecrypt")
	}
	resp, err := c.Client.TenantDEKDecrypt(ctx, &pb.TenantDEKDecryptRequest{
		TenantId:   req.TenantID,
		Ciphertext: req.Ciphertext,
		Aad:        req.AAD,
	})
	if err != nil {
		return nil, c.MapError("TenantDEKDecrypt", err)
	}
	return Decrypt(resp), nil
}

// Rewrap issues TenantDEKRewrap.
func (c Caller) Rewrap(ctx context.Context, req *transport.TenantDEKRewrapRequest) (*transport.TenantDEKRewrapResponse, error) {
	if req == nil {
		return nil, nilRequest("TenantDEKRewrap")
	}
	resp, err := c.Client.TenantDEKRewrap(ctx, &pb.TenantDEKRewrapRequest{
		TenantId:   req.TenantID,
		Ciphertext: req.Ciphertext,
		Aad:        req.AAD,
	})
	if err != nil {
		return nil, c.MapError("TenantDEKRewrap", err)
	}
	return Rewrap(resp), nil
}

// nilRequest is the error for a nil request struct.
func nilRequest(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}
