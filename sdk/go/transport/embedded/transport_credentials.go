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
)

// ---------------------------------------------------------------------------
// CredentialClient implementation
// ---------------------------------------------------------------------------

// CredentialSubmit submits a named credential value by delegating to the
// injected CredentialServicer.
func (t *Transport) CredentialSubmit(ctx context.Context, req *transport.CredentialSubmitRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return &NilServiceError{Service: "CredentialServicer"}
	}
	return t.credentialSvc.CredentialSubmit(ctx, req.Name, []byte(req.Value))
}

// CredentialStrategy returns the active credential management strategy by
// delegating to the injected CredentialServicer.
func (t *Transport) CredentialStrategy(ctx context.Context) (*transport.CredentialStrategyResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return nil, &NilServiceError{Service: "CredentialServicer"}
	}
	strategy := t.credentialSvc.CredentialStrategy()
	return &transport.CredentialStrategyResponse{
		Strategy: strategy,
	}, nil
}

// CredentialDelete removes a named credential by delegating to the injected
// CredentialServicer.
func (t *Transport) CredentialDelete(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.credentialSvc == nil {
		return &NilServiceError{Service: "CredentialServicer"}
	}
	return t.credentialSvc.CredentialDelete(ctx, name)
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

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// WithCredentialService injects a CredentialServicer into the transport.
func WithCredentialService(svc service.CredentialServicer) Option {
	return func(t *Transport) {
		t.credentialSvc = svc
	}
}
