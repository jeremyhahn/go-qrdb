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
)

// Compile-time interface assertion.
var _ transport.CredentialClient = (*Transport)(nil)

// nilCredentialRequestError is returned when a credential client method is
// invoked with a nil request struct. It exposes the calling method name so
// callers can match by typed assertion.
type nilCredentialRequestError struct {
	Method string
}

// Error returns a human-readable description of the nil-request failure.
func (e *nilCredentialRequestError) Error() string {
	return "transport: " + e.Method + ": nil request"
}

// ---------------------------------------------------------------------------
// CredentialClient implementation
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value via the
// credential.Submit handler. The wire payload uses the canonical
// CredentialSubmitRequest protobuf message.
func (t *Transport) CredentialSubmit(ctx context.Context, req *transport.CredentialSubmitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if req == nil {
		return &transport.RequestError{Method: "CredentialSubmit", Err: &nilCredentialRequestError{Method: "CredentialSubmit"}}
	}

	payload, err := proto.Marshal(&pb.CredentialSubmitRequest{
		Name:  req.Name,
		Value: req.Value,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	if _, err := t.call(ctx, "credential.Submit", payload); err != nil {
		return err
	}
	return nil
}

// CredentialStrategy returns the active credential management strategy
// name reported by the server via the credential.Strategy handler.
func (t *Transport) CredentialStrategy(ctx context.Context) (*transport.CredentialStrategyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	respBytes, err := t.call(ctx, "credential.Strategy", nil)
	if err != nil {
		return nil, err
	}

	var resp pb.CredentialStrategyResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.CredentialStrategyResponse{
		Strategy: resp.GetStrategy(),
	}, nil
}

// CredentialDelete removes a named credential and any non-current
// historical versions through the credential.Delete handler.
func (t *Transport) CredentialDelete(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := proto.Marshal(&pb.CredentialDeleteRequest{
		Name: name,
	})
	if err != nil {
		return &marshalError{Err: err}
	}

	if _, err := t.call(ctx, "credential.Delete", payload); err != nil {
		return err
	}
	return nil
}

// CredentialList returns the sorted set of stored credential names by
// invoking the credential.List handler. Values are never enumerated.
func (t *Transport) CredentialList(ctx context.Context, req *transport.CredentialListRequest) (*transport.CredentialListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	// CredentialListRequest is intentionally empty today, but is still
	// marshaled so that future filter fields remain wire-compatible. A
	// nil pointer is treated as the empty request.
	pbReq := &pb.CredentialListRequest{}
	_ = req

	payload, err := proto.Marshal(pbReq)
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "credential.List", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.CredentialListResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.CredentialListResponse{
		Names: resp.GetNames(),
	}, nil
}

// CredentialRotate atomically replaces the current credential value via
// the credential.Rotate handler. GracePeriodSeconds == 0 wipes the
// previous version immediately.
func (t *Transport) CredentialRotate(ctx context.Context, req *transport.CredentialRotateRequest) (*transport.CredentialRotateResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRotate", Err: &nilCredentialRequestError{Method: "CredentialRotate"}}
	}

	payload, err := proto.Marshal(&pb.CredentialRotateRequest{
		Name:               req.Name,
		NewValue:           req.NewValue,
		GracePeriodSeconds: req.GracePeriodSeconds,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "credential.Rotate", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.CredentialRotateResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.CredentialRotateResponse{
		Name:            resp.GetName(),
		PreviousVersion: resp.GetPreviousVersion(),
		NewVersion:      resp.GetNewVersion(),
		GraceUntilUnix:  resp.GetGraceUntilUnix(),
	}, nil
}

// CredentialRetrieveVersion returns the stored credential value for a
// specific version via the credential.RetrieveVersion handler. Version
// == 0 means "current version".
func (t *Transport) CredentialRetrieveVersion(ctx context.Context, req *transport.CredentialRetrieveVersionRequest) (*transport.CredentialRetrieveVersionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialRetrieveVersion", Err: &nilCredentialRequestError{Method: "CredentialRetrieveVersion"}}
	}

	payload, err := proto.Marshal(&pb.CredentialRetrieveVersionRequest{
		Name:    req.Name,
		Version: req.Version,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "credential.RetrieveVersion", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.CredentialRetrieveVersionResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.CredentialRetrieveVersionResponse{
		Value: resp.GetValue(),
	}, nil
}

// CredentialListVersions returns the version history (current + grace
// versions) for a credential via the credential.ListVersions handler.
// Value bytes are intentionally never returned by this call.
func (t *Transport) CredentialListVersions(ctx context.Context, req *transport.CredentialListVersionsRequest) (*transport.CredentialListVersionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialListVersions", Err: &nilCredentialRequestError{Method: "CredentialListVersions"}}
	}

	payload, err := proto.Marshal(&pb.CredentialListVersionsRequest{
		Name: req.Name,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "credential.ListVersions", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.CredentialListVersionsResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}

	pbVersions := resp.GetVersions()
	out := make([]transport.CredentialVersionInfo, 0, len(pbVersions))
	for _, v := range pbVersions {
		out = append(out, transport.CredentialVersionInfo{
			Version:        v.GetVersion(),
			IsCurrent:      v.GetIsCurrent(),
			CreatedAtUnix:  v.GetCreatedAtUnix(),
			GraceUntilUnix: v.GetGraceUntilUnix(),
		})
	}
	return &transport.CredentialListVersionsResponse{
		Name:     resp.GetName(),
		Versions: out,
	}, nil
}

// CredentialGCExpired reaps historical credential versions whose grace
// period has elapsed via the credential.GCExpired handler. Returns the
// number of versions reaped.
func (t *Transport) CredentialGCExpired(ctx context.Context, req *transport.CredentialGCRequest) (*transport.CredentialGCResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "CredentialGCExpired", Err: &nilCredentialRequestError{Method: "CredentialGCExpired"}}
	}

	payload, err := proto.Marshal(&pb.CredentialGCRequest{
		Name: req.Name,
	})
	if err != nil {
		return nil, &marshalError{Err: err}
	}

	respBytes, err := t.call(ctx, "credential.GCExpired", payload)
	if err != nil {
		return nil, err
	}

	var resp pb.CredentialGCResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return &transport.CredentialGCResponse{
		Reaped: int(resp.GetReaped()),
	}, nil
}

// CredentialStore persists a credential value via the credential.Store handler.
func (t *Transport) CredentialStore(ctx context.Context, name string, value []byte) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.CredentialStoreRequest{Name: name, Value: string(value)})
	if err != nil {
		return &marshalError{Err: err}
	}
	if _, err := t.call(ctx, "credential.Store", payload); err != nil {
		return err
	}
	return nil
}

// CredentialRetrieve returns the credential value via the credential.Retrieve handler.
func (t *Transport) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	payload, err := proto.Marshal(&pb.CredentialRetrieveRequest{Name: name})
	if err != nil {
		return nil, &marshalError{Err: err}
	}
	respBytes, err := t.call(ctx, "credential.Retrieve", payload)
	if err != nil {
		return nil, err
	}
	var resp pb.CredentialRetrieveResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		return nil, &unmarshalError{Err: err}
	}
	return []byte(resp.GetValue()), nil
}
