// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authtest_test

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net/url"
	"runtime"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// fatalRecorder is a testing.TB whose Fatalf records the message and stops
// the calling goroutine, as testing.T's does, without failing the test.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// runFatal calls fn with a fatalRecorder on its own goroutine and returns
// the Fatalf message, or "" when fn returned without one.
func runFatal(fn func(testing.TB)) string {
	rec := &fatalRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(rec)
	}()
	<-done
	return rec.msg
}

func TestClientSPIFFEURI_ParsesBackToTheTenantAndName(t *testing.T) {
	u := authtest.ClientSPIFFEURI(t, "acme", "alice")
	id, ok, err := spiffe.ParseID(u)
	if err != nil || !ok {
		t.Fatalf("ParseID(%v) = (%+v, %v, %v), want a tenant id", u, id, ok, err)
	}
	want := spiffe.ID{TrustDomain: "acme", Kind: spiffe.KindUser, Name: "alice"}
	if id != want {
		t.Fatalf("ParseID = %+v, want %+v", id, want)
	}
}

func TestClientSPIFFEURI_UnrepresentableIdentityFails(t *testing.T) {
	msg := runFatal(func(tb testing.TB) { authtest.ClientSPIFFEURI(tb, "a/b", "alice") })
	if msg == "" {
		t.Fatal("a tenant with a path separator produced a SPIFFE ID")
	}
}

func TestSetClientTenant_CertificateIsAccepted(t *testing.T) {
	tmpl := &x509.Certificate{Subject: pkix.Name{CommonName: "alice"}}
	authtest.SetClientTenant(t, tmpl, "acme", "alice")
	got, err := auth.TenantFromClientCert(tmpl, authtest.ClientTrustDomain)
	if err != nil || got != "acme" {
		t.Fatalf("TenantFromClientCert = (%q, %v), want (\"acme\", nil)", got, err)
	}
}

func TestSetClientTenant_ReplacesEarlierTenantFields(t *testing.T) {
	stale := &url.URL{Scheme: "spiffe", Host: "other", Path: "/user/alice"}
	tmpl := &x509.Certificate{
		Subject: pkix.Name{CommonName: "alice", OrganizationalUnit: []string{"other", "extra"}},
		URIs:    []*url.URL{stale},
	}
	authtest.SetClientTenant(t, tmpl, "acme", "alice")
	if len(tmpl.Subject.OrganizationalUnit) != 1 || tmpl.Subject.OrganizationalUnit[0] != "acme" {
		t.Fatalf("OU = %v, want [acme]", tmpl.Subject.OrganizationalUnit)
	}
	if len(tmpl.URIs) != 1 || tmpl.URIs[0].String() != authtest.ClientSPIFFEURI(t, "acme", "alice").String() {
		t.Fatalf("URIs = %v, want only the acme SPIFFE ID", tmpl.URIs)
	}
}

func TestSetClientTenant_UnrepresentableIdentityFails(t *testing.T) {
	msg := runFatal(func(tb testing.TB) {
		authtest.SetClientTenant(tb, &x509.Certificate{}, "acme", "a/b")
	})
	if msg == "" {
		t.Fatal("a name with a path separator produced a SPIFFE ID")
	}
}
