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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
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

func TestTransport_CredentialRetrieveBuffer_RoundTrip(t *testing.T) {
	var gotName string
	svc := &mockCredentialSvc{
		retrieveFn: func(_ context.Context, name string) ([]byte, error) {
			gotName = name
			return []byte("current-data"), nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "db")
	if err != nil {
		t.Fatalf("CredentialRetrieveBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != "current-data" {
		t.Errorf("value = %q, want current-data", got)
	}
	if gotName != "db" {
		t.Errorf("server got name %q", gotName)
	}
}

func TestTransport_CredentialRetrieveBuffer_ServerError(t *testing.T) {
	svc := &mockCredentialSvc{
		retrieveFn: func(context.Context, string) ([]byte, error) {
			return nil, credentials.ErrCredentialNotFound
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveBuffer(context.Background(), "absent")
	if err == nil {
		t.Fatal("expected error from server")
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransport_CredentialRetrieveVersionBuffer_RoundTrip(t *testing.T) {
	var gotName string
	var gotVersion uint64
	svc := &mockCredentialSvc{
		retrieveVersionFn: func(_ context.Context, name string, version uint64) ([]byte, error) {
			gotName = name
			gotVersion = version
			return []byte("v7-data"), nil
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "db", Version: 7,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != "v7-data" {
		t.Errorf("value = %q, want v7-data", got)
	}
	if gotName != "db" || gotVersion != 7 {
		t.Errorf("server got name=%q version=%d", gotName, gotVersion)
	}
}

func TestTransport_CredentialRetrieveVersionBuffer_VersionExpired(t *testing.T) {
	svc := &mockCredentialSvc{
		retrieveVersionFn: func(context.Context, string, uint64) ([]byte, error) {
			return nil, credentials.ErrVersionExpired
		},
	}
	tr, cleanup := connectTransportWithCredentials(t, svc)
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name: "x", Version: 1,
	})
	if err == nil {
		t.Fatal("expected error from server")
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransport_CredentialRetrieveVersionBuffer_NilRequest(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()

	buf, err := tr.CredentialRetrieveVersionBuffer(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestTransport_CredentialRetrieveBuffer_ClosedTransport(t *testing.T) {
	tr, cleanup := connectTransportWithCredentials(t, &mockCredentialSvc{})
	defer cleanup()
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cases := map[string]func() (*secure.Buffer, error){
		"Retrieve": func() (*secure.Buffer, error) {
			return tr.CredentialRetrieveBuffer(context.Background(), "x")
		},
		"RetrieveVersion": func() (*secure.Buffer, error) {
			return tr.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "x"})
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			buf, err := call()
			var closed *transport.ClosedError
			if !errors.As(err, &closed) {
				t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
			}
			if buf != nil {
				t.Error("a failed retrieve returned a buffer")
			}
		})
	}
}
