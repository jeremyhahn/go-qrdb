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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// bufferText reads buf's value inside its own borrow and destroys it.
func bufferText(t *testing.T, buf *secure.Buffer) string {
	t.Helper()
	if buf == nil {
		t.Fatal("buffer is nil")
	}
	defer buf.Destroy()
	var text string
	if err := buf.WithBytes(func(b []byte) error {
		text = string(b)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	return text
}

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

func TestEmbeddedCredential_RetrieveBuffer(t *testing.T) {
	var capturedName string
	svc := &mockCredentialService{
		retrieveFn: func(_ context.Context, name string) ([]byte, error) {
			capturedName = name
			return []byte("current-secret"), nil
		},
	}
	tr := newCredentialTransport(t, svc)

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "db-password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := bufferText(t, buf); got != "current-secret" {
		t.Errorf("value = %q, want current-secret", got)
	}
	if capturedName != "db-password" {
		t.Errorf("captured name = %q, want db-password", capturedName)
	}
}

func TestEmbeddedCredential_RetrieveBuffer_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "x")
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestEmbeddedCredential_RetrieveBuffer_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "x")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestEmbeddedCredential_RetrieveBuffer_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		retrieveFn: func(context.Context, string) ([]byte, error) { return nil, errCredential },
	}
	tr := newCredentialTransport(t, svc)

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "x")
	if !errors.Is(err, errCredential) {
		t.Fatalf("expected the service error, got %v", err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestEmbeddedCredential_RetrieveVersionBuffer(t *testing.T) {
	var capturedName string
	var capturedVersion uint64
	svc := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, name string, version uint64) ([]byte, error) {
			capturedName = name
			capturedVersion = version
			return []byte("secret-v2"), nil
		},
	}
	tr := newCredentialTransport(t, svc)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "db-password",
		Version: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := bufferText(t, buf); got != "secret-v2" {
		t.Errorf("value = %q, want secret-v2", got)
	}
	if capturedName != "db-password" || capturedVersion != 2 {
		t.Errorf("service saw name=%q version=%d", capturedName, capturedVersion)
	}
}

func TestEmbeddedCredential_RetrieveVersionBuffer_NilService(t *testing.T) {
	tr := newCredentialTransport(t, nil)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
	var nilErr *NilServiceError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestEmbeddedCredential_RetrieveVersionBuffer_NilRequest(t *testing.T) {
	tr := newCredentialTransport(t, &mockCredentialService{})

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), nil)
	var nilReqErr *NilRequestError
	if !errors.As(err, &nilReqErr) {
		t.Fatalf("expected NilRequestError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestEmbeddedCredential_RetrieveVersionBuffer_Closed(t *testing.T) {
	tr := closedCredentialTransport(t)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestEmbeddedCredential_RetrieveVersionBuffer_ServiceError(t *testing.T) {
	svc := &mockCredentialService{
		retrieveVersionFn: func(context.Context, string, uint64) ([]byte, error) { return nil, errCredential },
	}
	tr := newCredentialTransport(t, svc)

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
	if !errors.Is(err, errCredential) {
		t.Fatalf("expected the service error, got %v", err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}
