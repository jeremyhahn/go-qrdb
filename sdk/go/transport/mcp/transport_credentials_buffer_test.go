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

package mcp

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

func TestCredentialRetrieveBuffer_Success(t *testing.T) {
	tr, cleanup := connectCredentialInProcess(t, newMockCredentialSvc())
	defer cleanup()

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "db-password")
	if err != nil {
		t.Fatalf("CredentialRetrieveBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != "value" {
		t.Errorf("value = %q, want value", got)
	}
}

func TestCredentialRetrieveBuffer_Closed(t *testing.T) {
	tr, cleanup := connectCredentialInProcess(t, newMockCredentialSvc())
	defer cleanup()
	_ = tr.Close()

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "n")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveVersionBuffer_Success(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.retrievedValue = []byte("specific-v2")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "db-password",
		Version: 2,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != "specific-v2" {
		t.Errorf("value = %q, want specific-v2", got)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.lastRetrieveName != "db-password" || svc.lastRetrieveVersion != 2 {
		t.Errorf("service saw name=%q version=%d", svc.lastRetrieveName, svc.lastRetrieveVersion)
	}
}

func TestCredentialRetrieveVersionBuffer_BinaryValue(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.retrievedValue = nonUTF8CredentialValue
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "bin"})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != string(nonUTF8CredentialValue) {
		t.Errorf("value = %#v, want %#v", got, nonUTF8CredentialValue)
	}
}

func TestCredentialRetrieveVersionBuffer_ServiceError(t *testing.T) {
	svc := newMockCredentialSvc()
	svc.retrieveVersionErr = errors.New("version expired")
	tr, cleanup := connectCredentialInProcess(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "n", Version: 1})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveVersionBuffer_NilRequest(t *testing.T) {
	tr, cleanup := connectCredentialInProcess(t, newMockCredentialSvc())
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveVersionBuffer_Closed(t *testing.T) {
	tr, cleanup := connectCredentialInProcess(t, newMockCredentialSvc())
	defer cleanup()
	_ = tr.Close()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "n"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}
