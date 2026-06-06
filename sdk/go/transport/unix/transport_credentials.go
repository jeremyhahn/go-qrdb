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

package unix

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion. The Unix transport satisfies the full
// CredentialClient surface (3 legacy + 5 versioned-rotation methods); zero
// NotImplementedError returns are permitted by platform CLAUDE.md.
var _ transport.CredentialClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// CredentialClient implementation
//
// All eight methods are thin gRPC-over-UDS wrappers around the protobuf
// CredentialXxx RPCs on the QRDBService. Server-side handlers live in
// pkg/api/grpc/service_credentials.go and delegate to
// service.CredentialServicer (which the Unix server wires via
// WithCredentialService -> WithRegisterCredentialService).
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value via the
// QRDBService/CredentialSubmit RPC over the Unix socket.
func (t *Transport) CredentialSubmit(ctx context.Context, req *transport.CredentialSubmitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "CredentialSubmit", Err: errNilRequest}
	}

	_, err := t.client.CredentialSubmit(ctx, &pb.CredentialSubmitRequest{
		Name:  req.Name,
		Value: req.Value,
	})
	if err != nil {
		return mapGRPCError("CredentialSubmit", err)
	}
	return nil
}

// CredentialStrategy returns the active credential management strategy
// via the QRDBService/CredentialStrategy RPC over the Unix socket.
func (t *Transport) CredentialStrategy(ctx context.Context) (*transport.CredentialStrategyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.CredentialStrategy(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("CredentialStrategy", err)
	}
	return &transport.CredentialStrategyResponse{Strategy: resp.GetStrategy()}, nil
}

// CredentialDelete removes a named credential and its non-current
// historical versions via the QRDBService/CredentialDelete RPC over the
// Unix socket.
func (t *Transport) CredentialDelete(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if name == "" {
		return &transport.RequestError{Method: "CredentialDelete", Err: errNilRequest}
	}

	_, err := t.client.CredentialDelete(ctx, &pb.CredentialDeleteRequest{Name: name})
	if err != nil {
		return mapGRPCError("CredentialDelete", err)
	}
	return nil
}

// CredentialList returns the names of every stored credential via the
// QRDBService/CredentialList RPC over the Unix socket. Values are never
// returned by enumeration.
func (t *Transport) CredentialList(ctx context.Context, req *transport.CredentialListRequest) (*transport.CredentialListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialList", Err: errNilRequest}
	}

	resp, err := t.client.CredentialList(ctx, &pb.CredentialListRequest{})
	if err != nil {
		return nil, mapGRPCError("CredentialList", err)
	}
	names := resp.GetNames()
	if names == nil {
		names = []string{}
	}
	return &transport.CredentialListResponse{Names: names}, nil
}

// CredentialRotate atomically replaces the current value of a credential
// via the QRDBService/CredentialRotate RPC over the Unix socket. When
// GracePeriodSeconds > 0 the previous version remains retrievable for
// the specified duration.
func (t *Transport) CredentialRotate(ctx context.Context, req *transport.CredentialRotateRequest) (*transport.CredentialRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: errNilRequest}
	}

	resp, err := t.client.CredentialRotate(ctx, &pb.CredentialRotateRequest{
		Name:               req.Name,
		NewValue:           req.NewValue,
		GracePeriodSeconds: req.GracePeriodSeconds,
	})
	if err != nil {
		return nil, mapGRPCError("CredentialRotate", err)
	}
	return &transport.CredentialRotateResponse{
		Name:            resp.GetName(),
		PreviousVersion: resp.GetPreviousVersion(),
		NewVersion:      resp.GetNewVersion(),
		GraceUntilUnix:  resp.GetGraceUntilUnix(),
	}, nil
}

// CredentialRetrieveVersion returns the stored credential value for a
// specific version via the QRDBService/CredentialRetrieveVersion RPC
// over the Unix socket. Use Version == 0 to retrieve the current version.
func (t *Transport) CredentialRetrieveVersion(ctx context.Context, req *transport.CredentialRetrieveVersionRequest) (*transport.CredentialRetrieveVersionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: errNilRequest}
	}

	resp, err := t.client.CredentialRetrieveVersion(ctx, &pb.CredentialRetrieveVersionRequest{
		Name:    req.Name,
		Version: req.Version,
	})
	if err != nil {
		return nil, mapGRPCError("CredentialRetrieveVersion", err)
	}
	return &transport.CredentialRetrieveVersionResponse{Value: resp.GetValue()}, nil
}

// CredentialListVersions returns the version history (current + grace)
// for a credential via the QRDBService/CredentialListVersions RPC over
// the Unix socket. Each returned entry is metadata-only; values are
// never included.
func (t *Transport) CredentialListVersions(ctx context.Context, req *transport.CredentialListVersionsRequest) (*transport.CredentialListVersionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: errNilRequest}
	}

	resp, err := t.client.CredentialListVersions(ctx, &pb.CredentialListVersionsRequest{Name: req.Name})
	if err != nil {
		return nil, mapGRPCError("CredentialListVersions", err)
	}

	versions := make([]transport.CredentialVersionInfo, 0, len(resp.GetVersions()))
	for _, v := range resp.GetVersions() {
		versions = append(versions, transport.CredentialVersionInfo{
			Version:        v.GetVersion(),
			IsCurrent:      v.GetIsCurrent(),
			CreatedAtUnix:  v.GetCreatedAtUnix(),
			GraceUntilUnix: v.GetGraceUntilUnix(),
		})
	}
	return &transport.CredentialListVersionsResponse{
		Name:     resp.GetName(),
		Versions: versions,
	}, nil
}

// CredentialGCExpired reaps historical versions whose grace period has
// elapsed via the QRDBService/CredentialGCExpired RPC over the Unix
// socket. Returns the count of versions reaped.
func (t *Transport) CredentialGCExpired(ctx context.Context, req *transport.CredentialGCRequest) (*transport.CredentialGCResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: errNilRequest}
	}

	resp, err := t.client.CredentialGCExpired(ctx, &pb.CredentialGCRequest{Name: req.Name})
	if err != nil {
		return nil, mapGRPCError("CredentialGCExpired", err)
	}
	return &transport.CredentialGCResponse{Reaped: int(resp.GetReaped())}, nil
}

// CredentialStore persists a credential value via the CredentialStore RPC.
func (t *Transport) CredentialStore(ctx context.Context, name string, value []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if _, err := t.client.CredentialStore(ctx, &pb.CredentialStoreRequest{Name: name, Value: string(value)}); err != nil {
		return mapGRPCError("CredentialStore", err)
	}
	return nil
}

// CredentialRetrieve returns the credential value via the CredentialRetrieve RPC.
func (t *Transport) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	resp, err := t.client.CredentialRetrieve(ctx, &pb.CredentialRetrieveRequest{Name: name})
	if err != nil {
		return nil, mapGRPCError("CredentialRetrieve", err)
	}
	return []byte(resp.GetValue()), nil
}
