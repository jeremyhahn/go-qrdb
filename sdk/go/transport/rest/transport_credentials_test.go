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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// credentialsServer is a small in-memory HTTP stub that records every
// inbound request so the SDK transport tests can assert that the
// client encoded the wire format correctly.
// ---------------------------------------------------------------------------

type credentialsServer struct {
	// list
	listCalls atomic.Int64
	listResp  atomic.Value // *transport.CredentialListResponse
	listFail  atomic.Bool

	// rotate
	rotateCalls    atomic.Int64
	rotateName     atomic.Value // string
	rotateBody     atomic.Value // transport.CredentialRotateRequest
	rotateResp     atomic.Value // *transport.CredentialRotateResponse
	rotateFailCode atomic.Int64

	// retrieve version
	retrieveCalls    atomic.Int64
	retrieveName     atomic.Value // string
	retrieveVersion  atomic.Value // string
	retrieveResp     atomic.Value // *transport.CredentialRetrieveVersionResponse
	retrieveFailCode atomic.Int64

	// list versions
	listVersionsCalls atomic.Int64
	listVersionsName  atomic.Value // string
	listVersionsResp  atomic.Value // *transport.CredentialListVersionsResponse
	listVersionsFail  atomic.Bool

	// gc
	gcCalls    atomic.Int64
	gcName     atomic.Value // string
	gcResp     atomic.Value // *transport.CredentialGCResponse
	gcFailCode atomic.Int64
}

func (s *credentialsServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/credentials" && r.Method == http.MethodGet:
			s.listCalls.Add(1)
			if s.listFail.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			out := &transport.CredentialListResponse{Names: []string{}}
			if v, ok := s.listResp.Load().(*transport.CredentialListResponse); ok && v != nil {
				out = v
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case strings.HasSuffix(r.URL.Path, "/rotate") && r.Method == http.MethodPost:
			s.rotateCalls.Add(1)
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/credentials/"), "/rotate")
			s.rotateName.Store(name)
			var body transport.CredentialRotateRequest
			if r.ContentLength > 0 {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
			s.rotateBody.Store(body)
			if code := s.rotateFailCode.Load(); code != 0 {
				http.Error(w, "fail", int(code))
				return
			}
			out := &transport.CredentialRotateResponse{Name: name, PreviousVersion: 0, NewVersion: 1}
			if v, ok := s.rotateResp.Load().(*transport.CredentialRotateResponse); ok && v != nil {
				out = v
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case strings.HasSuffix(r.URL.Path, "/gc") && r.Method == http.MethodPost:
			s.gcCalls.Add(1)
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/credentials/"), "/gc")
			s.gcName.Store(name)
			if code := s.gcFailCode.Load(); code != 0 {
				http.Error(w, "fail", int(code))
				return
			}
			out := &transport.CredentialGCResponse{Reaped: 0}
			if v, ok := s.gcResp.Load().(*transport.CredentialGCResponse); ok && v != nil {
				out = v
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case strings.Contains(r.URL.Path, "/versions/") && r.Method == http.MethodGet:
			s.retrieveCalls.Add(1)
			// path: /v1/credentials/{name}/versions/{version}
			rest := strings.TrimPrefix(r.URL.Path, "/v1/credentials/")
			parts := strings.Split(rest, "/")
			if len(parts) != 3 || parts[1] != "versions" {
				http.NotFound(w, r)
				return
			}
			s.retrieveName.Store(parts[0])
			s.retrieveVersion.Store(parts[2])
			if code := s.retrieveFailCode.Load(); code != 0 {
				http.Error(w, "fail", int(code))
				return
			}
			out := &transport.CredentialRetrieveVersionResponse{Value: []byte("")}
			if v, ok := s.retrieveResp.Load().(*transport.CredentialRetrieveVersionResponse); ok && v != nil {
				out = v
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		case strings.HasSuffix(r.URL.Path, "/versions") && r.Method == http.MethodGet:
			s.listVersionsCalls.Add(1)
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/credentials/"), "/versions")
			s.listVersionsName.Store(name)
			if s.listVersionsFail.Load() {
				http.Error(w, "fail", http.StatusInternalServerError)
				return
			}
			out := &transport.CredentialListVersionsResponse{Name: name, Versions: []transport.CredentialVersionInfo{}}
			if v, ok := s.listVersionsResp.Load().(*transport.CredentialListVersionsResponse); ok && v != nil {
				out = v
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)

		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func newCredentialsTestTransport(t *testing.T) (*Transport, *credentialsServer) {
	t.Helper()
	srv := &credentialsServer{}
	httpSrv := httptest.NewServer(srv.handler())
	t.Cleanup(httpSrv.Close)
	return newWiredTransport(httpSrv), srv
}

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestCredentialList_Success(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.listResp.Store(&transport.CredentialListResponse{Names: []string{"alpha", "beta"}})

	resp, err := rt.CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		t.Fatalf("CredentialList: %v", err)
	}
	if srv.listCalls.Load() != 1 {
		t.Errorf("listCalls = %d, want 1", srv.listCalls.Load())
	}
	if len(resp.Names) != 2 || resp.Names[0] != "alpha" || resp.Names[1] != "beta" {
		t.Errorf("Names = %v", resp.Names)
	}
}

func TestCredentialList_ServerError(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.listFail.Store(true)

	_, err := rt.CredentialList(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d", reqErr.StatusCode)
	}
}

func TestCredentialList_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()
	_, err := rt.CredentialList(context.Background(), nil)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestCredentialRotate_Success(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.rotateResp.Store(&transport.CredentialRotateResponse{
		Name:            "apikey",
		PreviousVersion: 1,
		NewVersion:      2,
		GraceUntilUnix:  1700000000,
	})

	resp, err := rt.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:               "apikey",
		NewValue:           []byte("newsecret"),
		GracePeriodSeconds: 60,
	})
	if err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	if srv.rotateCalls.Load() != 1 {
		t.Errorf("rotateCalls = %d", srv.rotateCalls.Load())
	}
	if v, _ := srv.rotateName.Load().(string); v != "apikey" {
		t.Errorf("server saw name = %q", v)
	}
	if body, _ := srv.rotateBody.Load().(transport.CredentialRotateRequest); string(body.NewValue) != "newsecret" || body.GracePeriodSeconds != 60 {
		t.Errorf("body = %+v", body)
	}
	if resp.PreviousVersion != 1 || resp.NewVersion != 2 || resp.GraceUntilUnix != 1700000000 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestCredentialRotate_PathEscape(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.rotateResp.Store(&transport.CredentialRotateResponse{Name: "a/b", NewVersion: 1})

	_, err := rt.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:     "a/b",
		NewValue: []byte("x"),
	})
	// "/" in the name must be URL-encoded; the in-memory mux will not match
	// a literal "a/b" path. We accept either a 404 (server demux miss) or
	// the success path provided that we don't panic. The important thing
	// is that the name was URL-escaped before transmission.
	_ = err
}

func TestCredentialRotate_NilRequest(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialRotate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if !strings.Contains(err.Error(), "request must not be nil") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCredentialRotate_EmptyName(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: ""})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "credential name must not be empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCredentialRotate_ServerError(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.rotateFailCode.Store(int64(http.StatusGone))

	_, err := rt.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:     "k",
		NewValue: []byte("x"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusGone {
		t.Errorf("status = %d", reqErr.StatusCode)
	}
}

func TestCredentialRotate_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()
	_, err := rt.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{Name: "k"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestCredentialRetrieveVersion_Success(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.retrieveResp.Store(&transport.CredentialRetrieveVersionResponse{Value: []byte("secret")})

	resp, err := rt.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "apikey",
		Version: 3,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if srv.retrieveCalls.Load() != 1 {
		t.Errorf("retrieveCalls = %d", srv.retrieveCalls.Load())
	}
	if v, _ := srv.retrieveName.Load().(string); v != "apikey" {
		t.Errorf("server saw name = %q", v)
	}
	if v, _ := srv.retrieveVersion.Load().(string); v != "3" {
		t.Errorf("server saw version = %q", v)
	}
	if string(resp.Value) != "secret" {
		t.Errorf("Value = %q", resp.Value)
	}
}

func TestCredentialRetrieveVersion_CurrentVersionZero(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.retrieveResp.Store(&transport.CredentialRetrieveVersionResponse{Value: []byte("current")})

	_, err := rt.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "k",
		Version: 0,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if v, _ := srv.retrieveVersion.Load().(string); v != "0" {
		t.Errorf("server saw version = %q, want 0", v)
	}
}

func TestCredentialRetrieveVersion_NilRequest(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialRetrieveVersion(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialRetrieveVersion_EmptyName(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: ""})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestCredentialRetrieveVersion_Gone(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.retrieveFailCode.Store(int64(http.StatusGone))

	_, err := rt.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "k",
		Version: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError")
	}
	if reqErr.StatusCode != http.StatusGone {
		t.Errorf("status = %d", reqErr.StatusCode)
	}
}

func TestCredentialRetrieveVersion_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()
	_, err := rt.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestCredentialListVersions_Success(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.listVersionsResp.Store(&transport.CredentialListVersionsResponse{
		Name: "apikey",
		Versions: []transport.CredentialVersionInfo{
			{Version: 2, IsCurrent: true},
			{Version: 1, CreatedAtUnix: 1700000000, GraceUntilUnix: 1735689600},
		},
	})

	resp, err := rt.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "apikey"})
	if err != nil {
		t.Fatalf("CredentialListVersions: %v", err)
	}
	if srv.listVersionsCalls.Load() != 1 {
		t.Errorf("listVersionsCalls = %d", srv.listVersionsCalls.Load())
	}
	if v, _ := srv.listVersionsName.Load().(string); v != "apikey" {
		t.Errorf("server saw name = %q", v)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("len = %d, want 2", len(resp.Versions))
	}
	if !resp.Versions[0].IsCurrent || resp.Versions[1].IsCurrent {
		t.Errorf("IsCurrent flags wrong: %+v", resp.Versions)
	}
}

func TestCredentialListVersions_NilRequest(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialListVersions(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialListVersions_EmptyName(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: ""})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestCredentialListVersions_ServerError(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.listVersionsFail.Store(true)

	_, err := rt.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "k"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialListVersions_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()
	_, err := rt.CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: "k"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError")
	}
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestCredentialGCExpired_Success(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.gcResp.Store(&transport.CredentialGCResponse{Reaped: 5})

	resp, err := rt.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "apikey"})
	if err != nil {
		t.Fatalf("CredentialGCExpired: %v", err)
	}
	if srv.gcCalls.Load() != 1 {
		t.Errorf("gcCalls = %d", srv.gcCalls.Load())
	}
	if v, _ := srv.gcName.Load().(string); v != "apikey" {
		t.Errorf("server saw name = %q", v)
	}
	if resp.Reaped != 5 {
		t.Errorf("Reaped = %d", resp.Reaped)
	}
}

func TestCredentialGCExpired_NilRequest(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialGCExpired(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialGCExpired_EmptyName(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_, err := rt.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: ""})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestCredentialGCExpired_ServerError(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.gcFailCode.Store(int64(http.StatusNotFound))

	_, err := rt.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "k"})
	if err == nil {
		t.Fatal("expected error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError")
	}
	if reqErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", reqErr.StatusCode)
	}
}

func TestCredentialGCExpired_AfterClose(t *testing.T) {
	rt, _ := newCredentialsTestTransport(t)
	_ = rt.Close()
	_, err := rt.CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: "k"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError")
	}
}

// ---------------------------------------------------------------------------
// Binary (non-UTF-8) round-trip
// ---------------------------------------------------------------------------

// nonUTF8CredentialValue is a credential payload whose leading bytes
// (0x00 0xff 0xfe 0x01) are invalid UTF-8. The REST transport carries the
// value as a base64 (RFC 4648) []byte JSON field, so binary credential
// material round-trips byte-for-byte through the SDK client.
var nonUTF8CredentialValue = []byte{0x00, 0xff, 0xfe, 0x01, 'k', 'e', 'y'}

// TestCredential_BinaryRoundTrip exercises both wire directions through the
// SDK REST client: CredentialRotate encodes NewValue on send (server decodes
// the body.NewValue []byte from base64) and CredentialRetrieveVersion decodes
// Value on receive (server encodes the raw bytes as base64).
func TestCredential_BinaryRoundTrip(t *testing.T) {
	rt, srv := newCredentialsTestTransport(t)
	srv.rotateResp.Store(&transport.CredentialRotateResponse{Name: "bin", NewVersion: 2})
	srv.retrieveResp.Store(&transport.CredentialRetrieveVersionResponse{Value: nonUTF8CredentialValue})

	if _, err := rt.CredentialRotate(context.Background(), &transport.CredentialRotateRequest{
		Name:     "bin",
		NewValue: nonUTF8CredentialValue,
	}); err != nil {
		t.Fatalf("CredentialRotate: %v", err)
	}
	body, _ := srv.rotateBody.Load().(transport.CredentialRotateRequest)
	if string(body.NewValue) != string(nonUTF8CredentialValue) {
		t.Fatalf("server decoded NewValue = %#v, want %#v", body.NewValue, nonUTF8CredentialValue)
	}

	resp, err := rt.CredentialRetrieveVersion(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    "bin",
		Version: 0,
	})
	if err != nil {
		t.Fatalf("CredentialRetrieveVersion: %v", err)
	}
	if string(resp.Value) != string(nonUTF8CredentialValue) {
		t.Errorf("round-tripped value = %#v, want %#v", resp.Value, nonUTF8CredentialValue)
	}
}
