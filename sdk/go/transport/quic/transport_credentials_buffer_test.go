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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// retrieveBytes retrieves through the Buffer API and copies the value out for
// assertions, so tests exercise the supported path rather than the deprecated
// []byte method.
func (t *Transport) retrieveBytes(ctx context.Context, name string) ([]byte, error) {
	buf, err := t.CredentialRetrieveBuffer(ctx, name)
	if err != nil {
		return nil, err
	}
	defer buf.Destroy()
	return secure.CopyOut(buf)
}

// retrieveVersionResponse is retrieveBytes for a version, shaped as the wire
// DTO the []byte method returned.
func (t *Transport) retrieveVersionResponse(ctx context.Context, req *transport.CredentialRetrieveVersionRequest) (*transport.CredentialRetrieveVersionResponse, error) {
	buf, err := t.CredentialRetrieveVersionBuffer(ctx, req)
	if err != nil {
		return nil, err
	}
	defer buf.Destroy()
	value, err := secure.CopyOut(buf)
	if err != nil {
		return nil, err
	}
	return &transport.CredentialRetrieveVersionResponse{Value: value}, nil
}

func TestTransport_CredentialRetrieveBuffer_Closed(t *testing.T) {
	tr := newClosedCredentialTransport(t)

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "k")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransport_CredentialRetrieveVersionBuffer_Closed(t *testing.T) {
	tr := newClosedCredentialTransport(t)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransport_CredentialRetrieveVersionBuffer_NilRequest(t *testing.T) {
	tr := newOpenCredentialTransport(t)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), nil)
	var nre *nilCredentialRequestError
	if !errors.As(err, &nre) {
		t.Fatalf("expected *nilCredentialRequestError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}
