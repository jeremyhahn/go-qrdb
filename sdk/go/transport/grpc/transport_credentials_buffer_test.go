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

func TestTransportCredential_RetrieveBuffer_Success(t *testing.T) {
	addr, stop := startCredentialServer(t, &fakeCredentialServicer{})
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := bufferText(t, buf); got != "fake-value" {
		t.Errorf("unexpected value: %q", got)
	}
}

func TestTransportCredential_RetrieveBuffer_AfterClose(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}
	tr.closed.Store(true)

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "n")
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransportCredential_RetrieveVersionBuffer_Success(t *testing.T) {
	var capturedVersion uint64
	svc := &fakeCredentialServicer{
		retrieveVersFn: func(_ context.Context, _ string, version uint64) ([]byte, error) {
			capturedVersion = version
			return []byte("the-secret"), nil
		},
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "n", Version: 9,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := bufferText(t, buf); got != "the-secret" {
		t.Errorf("unexpected value: %q", got)
	}
	if capturedVersion != 9 {
		t.Errorf("expected version=9, got %d", capturedVersion)
	}
}

func TestTransportCredential_RetrieveVersionBuffer_ServerError(t *testing.T) {
	boom := errors.New("backend down")
	svc := &fakeCredentialServicer{
		retrieveVersFn: func(context.Context, string, uint64) ([]byte, error) { return nil, boom },
	}
	addr, stop := startCredentialServer(t, svc)
	defer stop()
	tr, cleanup := connectCredentialTransport(t, addr)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "n"})
	if err == nil {
		t.Fatal("expected an error from the server")
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransportCredential_RetrieveVersionBuffer_NilRequest(t *testing.T) {
	tr := &Transport{config: &transport.Config{}}

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), nil)
	var re *transport.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected RequestError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}
