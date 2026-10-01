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

package dekwire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
)

func TestStatusCarriesEveryVersion(t *testing.T) {
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	retired := created.Add(time.Hour)
	got := Status(&dek.Status{
		TenantID:  "acme",
		Algorithm: dek.Algorithm,
		Current:   2,
		Versions: []dek.VersionInfo{
			{Version: 1, CreatedAt: created, RetiredAt: &retired},
			{Version: 2, CreatedAt: created},
		},
	})
	if got.TenantID != "acme" || got.CurrentVersion != 2 || got.Algorithm != dek.Algorithm || len(got.Versions) != 2 {
		t.Fatalf("Status = %+v", got)
	}
	if got.Versions[0].RetiredAt == nil || !got.Versions[0].RetiredAt.Equal(retired) || got.Versions[1].RetiredAt != nil {
		t.Fatalf("versions = %+v", got.Versions)
	}
}

// TestEmptyStatusEncodesAnEmptyList: a tenant with no keyring yet must
// encode versions as [], which JSON clients iterate without a nil check.
func TestEmptyStatusEncodesAnEmptyList(t *testing.T) {
	raw, err := json.Marshal(Status(&dek.Status{TenantID: "acme", Algorithm: dek.Algorithm}))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"versions":[]`; !json.Valid(raw) || !strings.Contains(string(raw), want) {
		t.Fatalf("encoded %s, want it to contain %s", raw, want)
	}
}

func TestResultConversions(t *testing.T) {
	if e := Encrypt(&dek.Sealed{Ciphertext: []byte("c"), Version: 3}); string(e.Ciphertext) != "c" || e.Version != 3 {
		t.Fatalf("Encrypt = %+v", e)
	}
	if d := Decrypt(&dek.Opened{Plaintext: []byte("p"), Version: 2}); string(d.Plaintext) != "p" || d.Version != 2 {
		t.Fatalf("Decrypt = %+v", d)
	}
	if r := Rewrap(&dek.Rewrapped{Ciphertext: []byte("n"), FromVersion: 1, ToVersion: 4}); string(r.Ciphertext) != "n" || r.FromVersion != 1 || r.ToVersion != 4 {
		t.Fatalf("Rewrap = %+v", r)
	}
}

// TestCodeCoversEveryClass: each keyring error class maps to exactly the
// documented code and HTTP status, and a foreign error is left to the caller.
func TestCodeCoversEveryClass(t *testing.T) {
	cases := []struct {
		err  error
		code codes.Code
		http int
	}{
		{&dek.InvalidInputError{Op: "x", Field: "y"}, codes.InvalidArgument, 400},
		{&dek.CrossTenantError{}, codes.PermissionDenied, 403},
		{&dek.MalformedCiphertextError{Reason: "r"}, codes.InvalidArgument, 400},
		{&dek.AuthenticationFailedError{}, codes.InvalidArgument, 400},
		{&dek.UnknownVersionError{}, codes.NotFound, 404},
		{&dek.VersionRetiredError{}, codes.FailedPrecondition, 409},
		{&dek.RetireCurrentVersionError{}, codes.FailedPrecondition, 409},
		{&dek.KeyringUnavailableError{Err: errors.New("sealed")}, codes.Unavailable, 503},
		{&dek.CorruptKeyringError{}, codes.DataLoss, 500},
		{&dek.ConcurrentUpdateError{}, codes.Aborted, 409},
		{&dek.CryptoError{Op: "x", Err: errors.New("y")}, codes.Internal, 500},
	}
	for _, c := range cases {
		code, ok := Code(c.err)
		if !ok || code != c.code {
			t.Errorf("Code(%T) = %v, %v; want %v", c.err, code, ok, c.code)
		}
		status, ok := HTTPStatus(c.err)
		if !ok || status != c.http {
			t.Errorf("HTTPStatus(%T) = %d, %v; want %d", c.err, status, ok, c.http)
		}
	}
	if _, ok := Code(errors.New("foreign")); ok {
		t.Error("a foreign error must not be classified")
	}
	if _, ok := HTTPStatus(errors.New("foreign")); ok {
		t.Error("a foreign error must not get an HTTP status")
	}
}

func TestStatusProtoCarriesRetirement(t *testing.T) {
	created := time.Unix(100, 0)
	retired := time.Unix(200, 0)
	got := StatusProto(&dek.Status{TenantID: "acme", Algorithm: dek.Algorithm, Current: 2, Versions: []dek.VersionInfo{
		{Version: 1, CreatedAt: created, RetiredAt: &retired},
		{Version: 2, CreatedAt: created},
	}})
	if got.GetCurrentVersion() != 2 || got.GetVersions()[0].GetRetiredAtUnix() != 200 || got.GetVersions()[1].GetRetiredAtUnix() != 0 {
		t.Fatalf("StatusProto = %v", got)
	}
}
