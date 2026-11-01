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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// CredentialClient implementation
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value by delegating to the
// injected CredentialServicer.
func (t *Transport) CredentialSubmit(ctx context.Context, req *transport.CredentialSubmitRequest) error {
	ctx, err := t.admit(ctx, "CredentialSubmit")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "CredentialSubmit"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return &NilServiceError{Service: "CredentialServicer"}
	}
	return t.credentialSvc.CredentialSubmit(ctx, req.Name, req.Value)
}

// CredentialStrategy returns the active credential management strategy by
// delegating to the injected CredentialServicer.
func (t *Transport) CredentialStrategy(ctx context.Context) (*transport.CredentialStrategyResponse, error) {
	ctx, err := t.admit(ctx, "CredentialStrategy")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialStrategy"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	strategy, err := t.credentialSvc.CredentialStrategy(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.CredentialStrategyResponse{
		Strategy: strategy,
	}, nil
}

// CredentialDelete removes a named credential by delegating to the injected
// CredentialServicer.
func (t *Transport) CredentialDelete(ctx context.Context, name string) error {
	ctx, err := t.admit(ctx, "CredentialDelete")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "CredentialDelete"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return &NilServiceError{Service: "CredentialServicer"}
	}
	return t.credentialSvc.CredentialDelete(ctx, name)
}

// CredentialList returns the names of every stored credential by
// delegating to the injected CredentialServicer. Values are never
// returned by enumeration.
func (t *Transport) CredentialList(ctx context.Context, _ *transport.CredentialListRequest) (*transport.CredentialListResponse, error) {
	ctx, err := t.admit(ctx, "CredentialList")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialList"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	names, err := t.credentialSvc.CredentialList(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.CredentialListResponse{
		Names: names,
	}, nil
}

// CredentialRotate atomically replaces the current value of a credential,
// optionally keeping the previous version retrievable for a grace period.
// Delegates to the injected CredentialServicer.
func (t *Transport) CredentialRotate(ctx context.Context, req *transport.CredentialRotateRequest) (*transport.CredentialRotateResponse, error) {
	ctx, err := t.admit(ctx, "CredentialRotate")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialRotate"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	if req == nil {
		return nil, &NilRequestError{Method: "CredentialRotate"}
	}
	res, err := t.credentialSvc.CredentialRotate(ctx, req.Name, credentials.RotateOptions{
		NewValue:    req.NewValue,
		GracePeriod: time.Duration(req.GracePeriodSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	resp := &transport.CredentialRotateResponse{
		Name:            res.Name,
		PreviousVersion: res.PreviousVersion,
		NewVersion:      res.NewVersion,
	}
	if !res.GraceUntil.IsZero() {
		resp.GraceUntilUnix = res.GraceUntil.Unix()
	}
	return resp, nil
}

// CredentialListVersions returns the version history (current + non-expired
// grace) for a credential by delegating to the injected CredentialServicer.
// Values are never returned by this call.
func (t *Transport) CredentialListVersions(ctx context.Context, req *transport.CredentialListVersionsRequest) (*transport.CredentialListVersionsResponse, error) {
	ctx, err := t.admit(ctx, "CredentialListVersions")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialListVersions"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	if req == nil {
		return nil, &NilRequestError{Method: "CredentialListVersions"}
	}
	versions, err := t.credentialSvc.CredentialListVersions(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	out := make([]transport.CredentialVersionInfo, 0, len(versions))
	for _, v := range versions {
		entry := transport.CredentialVersionInfo{
			Version:   v.Version,
			IsCurrent: v.IsCurrent,
		}
		if !v.CreatedAt.IsZero() {
			entry.CreatedAtUnix = v.CreatedAt.Unix()
		}
		if !v.GraceUntil.IsZero() {
			entry.GraceUntilUnix = v.GraceUntil.Unix()
		}
		out = append(out, entry)
	}
	return &transport.CredentialListVersionsResponse{
		Name:     req.Name,
		Versions: out,
	}, nil
}

// CredentialGCExpired reaps historical versions of a credential whose
// grace period has elapsed, returning the count reaped. Delegates to the
// injected CredentialServicer.
func (t *Transport) CredentialGCExpired(ctx context.Context, req *transport.CredentialGCRequest) (*transport.CredentialGCResponse, error) {
	ctx, err := t.admit(ctx, "CredentialGCExpired")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialGCExpired"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	if req == nil {
		return nil, &NilRequestError{Method: "CredentialGCExpired"}
	}
	reaped, err := t.credentialSvc.CredentialGCExpired(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return &transport.CredentialGCResponse{
		Reaped: reaped,
	}, nil
}

// ---------------------------------------------------------------------------
// Request dispatch handlers
// ---------------------------------------------------------------------------

func handleCredentialSubmit(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.CredentialSubmitRequest)
	if !ok {
		return &InvalidRequestError{Method: "CredentialSubmit"}
	}
	return t.CredentialSubmit(ctx, r)
}

func handleCredentialStrategy(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.CredentialStrategy(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CredentialStrategyResponse); ok {
		*out = result
	}
	return nil
}

func handleCredentialDelete(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.CredentialDeleteRequest)
	if !ok {
		return &InvalidRequestError{Method: "CredentialDelete"}
	}
	return t.CredentialDelete(ctx, r.Name)
}

func handleCredentialList(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CredentialListRequest)
	if !ok {
		return &InvalidRequestError{Method: "CredentialList"}
	}
	result, err := t.CredentialList(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CredentialListResponse); ok {
		*out = result
	}
	return nil
}

func handleCredentialRotate(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CredentialRotateRequest)
	if !ok {
		return &InvalidRequestError{Method: "CredentialRotate"}
	}
	result, err := t.CredentialRotate(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CredentialRotateResponse); ok {
		*out = result
	}
	return nil
}

func handleCredentialListVersions(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CredentialListVersionsRequest)
	if !ok {
		return &InvalidRequestError{Method: "CredentialListVersions"}
	}
	result, err := t.CredentialListVersions(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CredentialListVersionsResponse); ok {
		*out = result
	}
	return nil
}

func handleCredentialGCExpired(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.CredentialGCRequest)
	if !ok {
		return &InvalidRequestError{Method: "CredentialGCExpired"}
	}
	result, err := t.CredentialGCExpired(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.CredentialGCResponse); ok {
		*out = result
	}
	return nil
}

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// WithCredentialService injects a CredentialServicer into the transport.
func WithCredentialService(svc service.CredentialServicer) Option {
	return func(t *Transport) {
		t.credentialSvc = svc
	}
}

// CredentialStore persists a credential value via the injected service.
func (t *Transport) CredentialStore(ctx context.Context, name string, value []byte) error {
	ctx, err := t.admit(ctx, "CredentialStore")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "CredentialStore"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return &NilServiceError{Service: "CredentialServicer"}
	}
	return t.credentialSvc.CredentialStore(ctx, name, value)
}

// CredentialRetrieveBuffer returns the credential value for name in protected
// memory by delegating to the injected CredentialServicer. Nothing is copied
// out: the embedded transport has no wire.
func (t *Transport) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	ctx, err := t.admit(ctx, "CredentialRetrieveBuffer")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialRetrieveBuffer"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	return t.credentialSvc.CredentialRetrieveBuffer(ctx, name)
}

// CredentialRetrieveVersionBuffer returns the stored value for a specific
// version in protected memory by delegating to the injected
// CredentialServicer. Nothing is copied out: the embedded transport has no
// wire.
func (t *Transport) CredentialRetrieveVersionBuffer(ctx context.Context, req *transport.CredentialRetrieveVersionRequest) (*secure.Buffer, error) {
	ctx, err := t.admit(ctx, "CredentialRetrieveVersionBuffer")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CredentialRetrieveVersionBuffer"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	if req == nil {
		return nil, &NilRequestError{Method: "CredentialRetrieveVersion"}
	}
	return t.credentialSvc.CredentialRetrieveVersionBuffer(ctx, req.Name, req.Version)
}
