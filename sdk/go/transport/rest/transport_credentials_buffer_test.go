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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// newCredentialValueTransport serves GET /v1/credentials/{name}/value with
// value, or with status when status is non-zero.
func newCredentialValueTransport(t *testing.T, value []byte, status int) *Transport {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/credentials/apikey/value", func(w http.ResponseWriter, _ *http.Request) {
		if status != 0 {
			http.Error(w, "fail", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string][]byte{"value": value})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newWiredTransport(srv)
}

func TestCredentialRetrieveBuffer_Success(t *testing.T) {
	rt := newCredentialValueTransport(t, nonUTF8CredentialValue, 0)

	buf, err := rt.CredentialRetrieveBuffer(context.Background(), "apikey")
	if err != nil {
		t.Fatalf("CredentialRetrieveBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != string(nonUTF8CredentialValue) {
		t.Errorf("value = %#v, want %#v", got, nonUTF8CredentialValue)
	}
}

func TestCredentialRetrieveBuffer_ServerError(t *testing.T) {
	rt := newCredentialValueTransport(t, nil, http.StatusInternalServerError)

	buf, err := rt.CredentialRetrieveBuffer(context.Background(), "apikey")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveBuffer_EmptyValueFailsClosed(t *testing.T) {
	rt := newCredentialValueTransport(t, []byte{}, 0)

	buf, err := rt.CredentialRetrieveBuffer(context.Background(), "apikey")
	var size *secure.InvalidSizeError
	if !errors.As(err, &size) {
		t.Fatalf("expected *secure.InvalidSizeError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("an empty wire value returned a buffer")
	}
}

func TestCredentialRetrieveBuffer_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()

	buf, err := rt.CredentialRetrieveBuffer(context.Background(), "apikey")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveVersionBuffer_Success(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.retrieveResp.Store(&transport.CredentialRetrieveVersionResponse{Value: []byte("secret")})

	buf, err := rt.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "apikey",
		Version: 3,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != "secret" {
		t.Errorf("value = %q", got)
	}
	if v, _ := srv.retrieveVersion.Load().(string); v != "3" {
		t.Errorf("server saw version = %q", v)
	}
}

func TestCredentialRetrieveVersionBuffer_BinaryValue(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.retrieveResp.Store(&transport.CredentialRetrieveVersionResponse{Value: nonUTF8CredentialValue})

	buf, err := rt.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "bin"})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersionBuffer: %v", err)
	}
	if got := bufferText(t, buf); got != string(nonUTF8CredentialValue) {
		t.Errorf("value = %#v, want %#v", got, nonUTF8CredentialValue)
	}
}

func TestCredentialRetrieveVersionBuffer_Gone(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.retrieveFailCode.Store(int64(http.StatusGone))

	buf, err := rt.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusGone {
		t.Errorf("status = %d", reqErr.StatusCode)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveVersionBuffer_NilRequest(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)

	buf, err := rt.CredentialRetrieveVersionBuffer(context.Background(), nil)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}

func TestCredentialRetrieveVersionBuffer_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()

	buf, err := rt.CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
	if buf != nil {
		t.Error("a failed retrieve returned a buffer")
	}
}
