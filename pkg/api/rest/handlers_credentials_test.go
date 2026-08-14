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

//go:build !integration

package rest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nonUTF8CredentialValue is a credential payload with bytes that are invalid
// UTF-8 (leading 0x00 0xff 0xfe 0x01). Credentials routinely carry such
// non-UTF-8 content (private keys, certificates, random tokens); these tests
// prove it round-trips byte-for-byte over the JSON REST transport, which
// carries the value as a base64 (RFC 4648) []byte field.
var nonUTF8CredentialValue = []byte{0x00, 0xff, 0xfe, 0x01, 'k', 'e', 'y'}

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestHandleCredentialList_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialList(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialList_Success(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.listFn = func(_ context.Context) ([]string, error) {
		return []string{"alpha", "beta"}, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialList(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp transport.CredentialListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Names) != 2 || resp.Names[0] != "alpha" || resp.Names[1] != "beta" {
		t.Errorf("names = %v, want [alpha beta]", resp.Names)
	}
}

func TestHandleCredentialList_EmptyNamesEncodedAsArray(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.listFn = func(_ context.Context) ([]string, error) {
		return nil, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialList(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"names":[]`) {
		t.Errorf("expected names:[] in body, got %s", rec.Body.String())
	}
}

func TestHandleCredentialList_EnumerationNotSupported(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.listFn = func(_ context.Context) ([]string, error) {
		return nil, credentials.ErrEnumerationNotSupported
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialList(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestHandleCredentialRotate_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/rotate", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "k")
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialRotate_EmptyName(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials//rotate", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialRotate_BadBody(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/rotate", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "k")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialRotate_Success(t *testing.T) {
	graceUntil := time.Unix(1735689600, 0).UTC() // 2025-01-01T00:00:00Z
	svc := newCoverageCredentialService()
	svc.rotateFn = func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
		if name != "apikey" {
			t.Errorf("name = %q, want apikey", name)
		}
		if string(opts.NewValue) != "newsecret" {
			t.Errorf("NewValue = %q, want newsecret", string(opts.NewValue))
		}
		if opts.GracePeriod != 60*time.Second {
			t.Errorf("GracePeriod = %v, want 60s", opts.GracePeriod)
		}
		return &credentials.RotateResult{
			Name:            name,
			PreviousVersion: 1,
			NewVersion:      2,
			GraceUntil:      graceUntil,
		}, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))

	body := `{"name":"ignored","new_value":"bmV3c2VjcmV0","grace_period_seconds":60}`
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/apikey/rotate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "apikey")
	assertStatus(t, rec, http.StatusOK)

	var resp transport.CredentialRotateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "apikey" {
		t.Errorf("Name = %q, want apikey", resp.Name)
	}
	if resp.PreviousVersion != 1 || resp.NewVersion != 2 {
		t.Errorf("versions = %d/%d, want 1/2", resp.PreviousVersion, resp.NewVersion)
	}
	if resp.GraceUntilUnix != graceUntil.Unix() {
		t.Errorf("GraceUntilUnix = %d, want %d", resp.GraceUntilUnix, graceUntil.Unix())
	}
}

func TestHandleCredentialRotate_NoGrace(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.rotateFn = func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
		if opts.GracePeriod != 0 {
			t.Errorf("GracePeriod = %v, want 0", opts.GracePeriod)
		}
		return &credentials.RotateResult{
			Name:            name,
			PreviousVersion: 0,
			NewVersion:      1,
		}, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))

	body := `{"new_value":"eA=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/rotate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "k")
	assertStatus(t, rec, http.StatusOK)

	var resp transport.CredentialRotateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.GraceUntilUnix != 0 {
		t.Errorf("GraceUntilUnix = %d, want 0 (omitted)", resp.GraceUntilUnix)
	}
}

func TestHandleCredentialRotate_InvalidGracePeriod(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.rotateFn = func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
		return nil, credentials.ErrInvalidGracePeriod
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/rotate", strings.NewReader(`{"new_value":"eA=="}`))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "k")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialRotate_EmptyValue(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.rotateFn = func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
		return nil, credentials.ErrEmptyCredentialValue
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/rotate", strings.NewReader(`{"new_value":""}`))
	rec := httptest.NewRecorder()
	s.handleCredentialRotate(rec, req, "k")
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestHandleCredentialRetrieveVersion_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/0", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "k", "0")
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialRetrieveVersion_InvalidVersion(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/abc", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "k", "abc")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialRetrieveVersion_EmptyName(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials//versions/0", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "", "0")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialRetrieveVersion_Success(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, name string, version uint64) ([]byte, error) {
		if name != "k" || version != 3 {
			t.Errorf("got name=%q version=%d, want k/3", name, version)
		}
		return []byte("hello"), nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/3", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "k", "3")
	assertStatus(t, rec, http.StatusOK)

	var resp transport.CredentialRetrieveVersionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(resp.Value) != "hello" {
		t.Errorf("Value = %q, want hello", resp.Value)
	}
}

func TestHandleCredentialRetrieveVersion_VersionExpired(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return nil, credentials.ErrVersionExpired
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/1", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "k", "1")
	assertStatus(t, rec, http.StatusGone)
}

func TestHandleCredentialRetrieveVersion_VersionNotFound(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return nil, credentials.ErrVersionNotFound
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/99", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "k", "99")
	assertStatus(t, rec, http.StatusGone)
}

func TestHandleCredentialRetrieveVersion_CredentialNotFound(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return nil, credentials.ErrCredentialNotFound
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/0", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rec, req, "k", "0")
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestHandleCredentialListVersions_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialListVersions(rec, req, "k")
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialListVersions_EmptyName(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials//versions", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialListVersions(rec, req, "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialListVersions_Success(t *testing.T) {
	created := time.Unix(1700000000, 0).UTC()
	grace := time.Unix(1735689600, 0).UTC()
	svc := newCoverageCredentialService()
	svc.listVersionsFn = func(_ context.Context, name string) ([]credentials.CredentialVersion, error) {
		return []credentials.CredentialVersion{
			{Name: name, Version: 2, IsCurrent: true},
			{Name: name, Version: 1, CreatedAt: created, GraceUntil: grace},
		}, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialListVersions(rec, req, "k")
	assertStatus(t, rec, http.StatusOK)

	var resp transport.CredentialListVersionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "k" {
		t.Errorf("Name = %q, want k", resp.Name)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("len = %d, want 2", len(resp.Versions))
	}
	if resp.Versions[0].Version != 2 || !resp.Versions[0].IsCurrent {
		t.Errorf("current = %+v", resp.Versions[0])
	}
	if resp.Versions[1].Version != 1 || resp.Versions[1].IsCurrent {
		t.Errorf("historical = %+v", resp.Versions[1])
	}
	if resp.Versions[1].CreatedAtUnix != created.Unix() {
		t.Errorf("CreatedAtUnix = %d, want %d", resp.Versions[1].CreatedAtUnix, created.Unix())
	}
	if resp.Versions[1].GraceUntilUnix != grace.Unix() {
		t.Errorf("GraceUntilUnix = %d, want %d", resp.Versions[1].GraceUntilUnix, grace.Unix())
	}
}

func TestHandleCredentialListVersions_NotFound(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.listVersionsFn = func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
		return nil, credentials.ErrCredentialNotFound
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialListVersions(rec, req, "k")
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestHandleCredentialGCExpired_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/gc", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialGCExpired(rec, req, "k")
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialGCExpired_EmptyName(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials//gc", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialGCExpired(rec, req, "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialGCExpired_Success(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.gcFn = func(_ context.Context, name string) (int, error) {
		if name != "k" {
			t.Errorf("name = %q, want k", name)
		}
		return 7, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/gc", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialGCExpired(rec, req, "k")
	assertStatus(t, rec, http.StatusOK)

	var resp transport.CredentialGCResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Reaped != 7 {
		t.Errorf("Reaped = %d, want 7", resp.Reaped)
	}
}

func TestHandleCredentialGCExpired_ServiceError(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.gcFn = func(_ context.Context, _ string) (int, error) {
		return 0, credentials.ErrCredentialNotFound
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/gc", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialGCExpired(rec, req, "k")
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleCredentialByPath dispatcher
// ---------------------------------------------------------------------------

func TestHandleCredentialByPath_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodDelete, "/v1/credentials/k", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialByPath_EmptyName(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodDelete, "/v1/credentials/", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialByPath_DispatchDelete(t *testing.T) {
	svc := newCoverageCredentialService()
	called := false
	svc.deleteFn = func(_ context.Context, name string) error {
		called = true
		if name != "k" {
			t.Errorf("name = %q, want k", name)
		}
		return nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodDelete, "/v1/credentials/k", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
	if !called {
		t.Error("deleteFn not invoked")
	}
}

func TestHandleCredentialByPath_DispatchRotate(t *testing.T) {
	svc := newCoverageCredentialService()
	called := false
	svc.rotateFn = func(_ context.Context, name string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
		called = true
		return &credentials.RotateResult{Name: name, PreviousVersion: 0, NewVersion: 1}, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/rotate", strings.NewReader(`{"new_value":"dg=="}`))
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !called {
		t.Error("rotateFn not invoked")
	}
}

func TestHandleCredentialByPath_DispatchGC(t *testing.T) {
	svc := newCoverageCredentialService()
	called := false
	svc.gcFn = func(_ context.Context, _ string) (int, error) {
		called = true
		return 0, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/k/gc", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !called {
		t.Error("gcFn not invoked")
	}
}

func TestHandleCredentialByPath_DispatchListVersions(t *testing.T) {
	svc := newCoverageCredentialService()
	called := false
	svc.listVersionsFn = func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
		called = true
		return []credentials.CredentialVersion{}, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !called {
		t.Error("listVersionsFn not invoked")
	}
}

func TestHandleCredentialByPath_DispatchRetrieveVersion(t *testing.T) {
	svc := newCoverageCredentialService()
	called := false
	svc.retrieveVersionFn = func(_ context.Context, _ string, version uint64) ([]byte, error) {
		called = true
		if version != 5 {
			t.Errorf("version = %d, want 5", version)
		}
		return []byte("v"), nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/5", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !called {
		t.Error("retrieveVersionFn not invoked")
	}
}

func TestHandleCredentialByPath_MethodNotAllowed(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPut, "/v1/credentials/k", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleCredentialByPath_RotateMethodNotAllowed(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/rotate", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleCredentialByPath_UnknownOp(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/bogus", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCredentialByPath_TooDeep(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/3/extra", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialByPath(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// mapCredentialHTTPError
// ---------------------------------------------------------------------------

func TestMapCredentialHTTPError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", credentials.ErrCredentialNotFound, http.StatusNotFound},
		{"enumeration", credentials.ErrEnumerationNotSupported, http.StatusNotImplemented},
		{"version not found", credentials.ErrVersionNotFound, http.StatusGone},
		{"version expired", credentials.ErrVersionExpired, http.StatusGone},
		{"grace negative", credentials.ErrInvalidGracePeriod, http.StatusBadRequest},
		{"empty name", credentials.ErrEmptyCredentialName, http.StatusBadRequest},
		{"empty value", credentials.ErrEmptyCredentialValue, http.StatusBadRequest},
		{"invalid strategy", credentials.ErrInvalidStrategy, http.StatusInternalServerError},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapCredentialHTTPError(c.err)
			if got != c.want {
				t.Errorf("mapCredentialHTTPError(%v) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Binary (non-UTF-8) round-trip
// ---------------------------------------------------------------------------

// TestHandleCredentialSubmitRetrieveVersion_BinaryRoundTrip proves the submit
// request body decodes a base64 value to raw bytes before reaching the service
// and the retrieve-version response re-encodes the raw bytes as base64, so a
// non-UTF-8 credential survives the JSON REST wire byte-for-byte.
func TestHandleCredentialSubmitRetrieveVersion_BinaryRoundTrip(t *testing.T) {
	var stored []byte
	svc := newCoverageCredentialService()
	svc.submitFn = func(_ context.Context, _ string, v []byte) error {
		stored = append([]byte(nil), v...)
		return nil
	}
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return stored, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithCredentialService(svc))

	body, err := json.Marshal(credentialSubmitRequest{Name: "k", Value: nonUTF8CredentialValue})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The wire body must carry the value as base64, never the raw bytes.
	if !strings.Contains(string(body), base64.StdEncoding.EncodeToString(nonUTF8CredentialValue)) {
		t.Fatalf("submit body is not base64-encoded: %s", body)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCredentialSubmit(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
	if !bytes.Equal(stored, nonUTF8CredentialValue) {
		t.Fatalf("service received %#v, want %#v", stored, nonUTF8CredentialValue)
	}

	rreq := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/versions/0", nil)
	rrec := httptest.NewRecorder()
	s.handleCredentialRetrieveVersion(rrec, rreq, "k", "0")
	assertStatus(t, rrec, http.StatusOK)

	var resp transport.CredentialRetrieveVersionResponse
	if err := json.NewDecoder(rrec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(resp.Value, nonUTF8CredentialValue) {
		t.Errorf("round-tripped value = %#v, want %#v", resp.Value, nonUTF8CredentialValue)
	}
}

// TestHandleCredentialStoreRetrieve_BinaryRoundTrip proves the store request
// body and the retrieve (value) response both carry the credential as a base64
// []byte field, preserving non-UTF-8 content byte-for-byte.
func TestHandleCredentialStoreRetrieve_BinaryRoundTrip(t *testing.T) {
	var stored []byte
	svc := newCoverageCredentialService()
	svc.storeFn = func(_ context.Context, _ string, v []byte) error {
		stored = append([]byte(nil), v...)
		return nil
	}
	svc.retrieveFn = func(_ context.Context, _ string) ([]byte, error) {
		return stored, nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithCredentialService(svc))

	body, err := json.Marshal(credentialStoreRequest{Name: "k", Value: nonUTF8CredentialValue})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/store", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCredentialStore(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
	if !bytes.Equal(stored, nonUTF8CredentialValue) {
		t.Fatalf("service received %#v, want %#v", stored, nonUTF8CredentialValue)
	}

	rreq := httptest.NewRequest(http.MethodGet, "/v1/credentials/k/value", nil)
	rrec := httptest.NewRecorder()
	s.handleCredentialRetrieve(rrec, rreq, "k")
	assertStatus(t, rrec, http.StatusOK)

	var resp credentialValueResponse
	if err := json.NewDecoder(rrec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(resp.Value, nonUTF8CredentialValue) {
		t.Errorf("round-tripped value = %#v, want %#v", resp.Value, nonUTF8CredentialValue)
	}
}
