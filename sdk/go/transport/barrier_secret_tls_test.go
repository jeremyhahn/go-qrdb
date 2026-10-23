// Copyright 2026 Jeremy Hahn
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

package transport

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const testSecret = "s3cr3t-value"

// TestBarrierSecretCarriers: every carrier reports the kind of the first secret
// it carries, and nothing for a nil request or one without a secret.
func TestBarrierSecretCarriers(t *testing.T) {
	b := []byte(testSecret)
	cases := []struct {
		name string
		req  BarrierSecretCarrier
		want BarrierSecretKind
	}{
		{"BarrierInitialize nil", (*BarrierInitializeRequest)(nil), ""},
		{"BarrierInitialize empty", &BarrierInitializeRequest{}, ""},
		{"BarrierInitialize secret", &BarrierInitializeRequest{Secret: b}, BarrierSecretInit},
		{"BarrierInitialize SO PIN", &BarrierInitializeRequest{SOPin: testSecret}, soPINSecret},
		{"BarrierInitialize user PIN", &BarrierInitializeRequest{UserPin: testSecret}, BarrierSecretUserPIN},
		{"BarrierInitialize secret first", &BarrierInitializeRequest{Secret: b, SOPin: testSecret, UserPin: testSecret}, BarrierSecretInit},
		{"BarrierUnseal nil", (*BarrierUnsealRequest)(nil), ""},
		{"BarrierUnseal empty", &BarrierUnsealRequest{}, ""},
		{"BarrierUnseal key", &BarrierUnsealRequest{Secret: b}, BarrierSecretUnsealKey},
		{"BarrierInitializeShamir nil", (*BarrierInitializeShamirRequest)(nil), ""},
		{"BarrierInitializeShamir empty", &BarrierInitializeShamirRequest{Threshold: 2}, ""},
		{"BarrierInitializeShamir SO PIN", &BarrierInitializeShamirRequest{SOPin: testSecret, UserPin: testSecret}, soPINSecret},
		{"BarrierInitializeShamir user PIN", &BarrierInitializeShamirRequest{UserPin: testSecret}, BarrierSecretUserPIN},
		{"BarrierEnableEncryption nil", (*BarrierEnableEncryptionRequest)(nil), ""},
		{"BarrierEnableEncryption empty", &BarrierEnableEncryptionRequest{TenantID: "acme"}, ""},
		{"BarrierEnableEncryption passphrase", &BarrierEnableEncryptionRequest{Passphrase: b}, BarrierSecretPassphrase},
		{"BarrierGenerateRecoveryKeys nil", (*BarrierGenerateRecoveryKeysRequest)(nil), ""},
		{"BarrierGenerateRecoveryKeys empty", &BarrierGenerateRecoveryKeysRequest{Threshold: 2}, ""},
		{"BarrierGenerateRecoveryKeys SO PIN", &BarrierGenerateRecoveryKeysRequest{SOPin: testSecret}, soPINSecret},
		{"QuorumSubmit nil", (*QuorumSubmitRequest)(nil), ""},
		{"QuorumSubmit empty", &QuorumSubmitRequest{Username: "officer"}, ""},
		{"QuorumSubmit share", &QuorumSubmitRequest{Share: b}, BarrierSecretShare},
		{"RekeyStart nil", (*RekeyStartRequest)(nil), ""},
		{"RekeyStart empty", &RekeyStartRequest{Threshold: 2}, ""},
		{"RekeyStart SO PIN", &RekeyStartRequest{SOPin: testSecret}, soPINSecret},
		{"OfficerRevoke nil", (*OfficerRevokeRequest)(nil), ""},
		{"OfficerRevoke empty", &OfficerRevokeRequest{Username: "officer"}, ""},
		{"OfficerRevoke SO PIN", &OfficerRevokeRequest{SOPin: testSecret}, soPINSecret},
		{"CeremonyInitialize nil", (*CeremonyInitializeRequest)(nil), ""},
		{"CeremonyInitialize empty", &CeremonyInitializeRequest{}, ""},
		{"CeremonyInitialize SO PIN first", &CeremonyInitializeRequest{SOPin: testSecret, UserPin: testSecret, Secret: b}, soPINSecret},
		{"CeremonyInitialize user PIN", &CeremonyInitializeRequest{UserPin: testSecret, Secret: b}, BarrierSecretUserPIN},
		{"CeremonyInitialize secret", &CeremonyInitializeRequest{Secret: b}, BarrierSecretInit},
		{"SignCSRInit nil", (*SignCSRInitRequest)(nil), ""},
		{"SignCSRInit empty", &SignCSRInitRequest{Username: "officer"}, ""},
		{"SignCSRInit SO PIN", &SignCSRInitRequest{SOPin: testSecret}, soPINSecret},
		{"TenantBarrierInit nil", (*TenantBarrierInitRequest)(nil), ""},
		{"TenantBarrierInit empty", &TenantBarrierInitRequest{TenantID: "acme"}, ""},
		{"TenantBarrierInit passphrase", &TenantBarrierInitRequest{Passphrase: b}, BarrierSecretPassphrase},
		{"TenantBarrierUnseal nil", (*TenantBarrierUnsealRequest)(nil), ""},
		{"TenantBarrierUnseal empty", &TenantBarrierUnsealRequest{TenantID: "acme"}, ""},
		{"TenantBarrierUnseal key", &TenantBarrierUnsealRequest{Key: b}, BarrierSecretUnsealKey},
		{"TenantQuorumSubmit nil", (*TenantQuorumSubmitRequest)(nil), ""},
		{"TenantQuorumSubmit empty", &TenantQuorumSubmitRequest{TenantID: "acme"}, ""},
		{"TenantQuorumSubmit share", &TenantQuorumSubmitRequest{Share: b}, BarrierSecretShare},
		{"DatabaseQuorumSubmit nil", (*DatabaseQuorumSubmitRequest)(nil), ""},
		{"DatabaseQuorumSubmit empty", &DatabaseQuorumSubmitRequest{Database: "db1"}, ""},
		{"DatabaseQuorumSubmit share", &DatabaseQuorumSubmitRequest{QuorumSubmitRequest: QuorumSubmitRequest{Share: b}}, BarrierSecretShare},
		{"DatabaseBarrierConfig nil", (*DatabaseBarrierConfig)(nil), ""},
		{"DatabaseBarrierConfig empty", &DatabaseBarrierConfig{Enabled: true}, ""},
		{"DatabaseBarrierConfig secret", &DatabaseBarrierConfig{Secret: testSecret}, BarrierSecretPassphrase},
		{"CreateDatabase no barrier", CreateDatabaseRequest{Name: "db1"}, ""},
		{"CreateDatabase barrier without secret", CreateDatabaseRequest{Barrier: &DatabaseBarrierConfig{Enabled: true}}, ""},
		{"CreateDatabase secret", CreateDatabaseRequest{Barrier: &DatabaseBarrierConfig{Secret: testSecret}}, BarrierSecretPassphrase},
		{"UpdateDatabaseBarrier no barrier", UpdateDatabaseBarrierRequest{Name: "db1"}, ""},
		{"UpdateDatabaseBarrier secret", UpdateDatabaseBarrierRequest{Barrier: &DatabaseBarrierConfig{Secret: testSecret}}, BarrierSecretPassphrase},
		{"UnsealDatabase without secret", DatabaseUnsealSecret(""), ""},
		{"UnsealDatabase secret", DatabaseUnsealSecret(testSecret), BarrierSecretUnsealKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.barrierSecret(); got != tc.want {
				t.Fatalf("barrierSecret() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequireTLSForBarrierSecret(t *testing.T) {
	withSecret := &BarrierUnsealRequest{Secret: []byte(testSecret)}
	cases := []struct {
		name    string
		tlsConn bool
		req     BarrierSecretCarrier
		refused bool
	}{
		{"secret over plaintext", false, withSecret, true},
		{"secret over TLS", true, withSecret, false},
		{"no secret over plaintext", false, &BarrierUnsealRequest{}, false},
		{"nil request over plaintext", false, (*BarrierUnsealRequest)(nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireTLSForBarrierSecret("grpc", tc.tlsConn, tc.req)
			var tlsErr *BarrierSecretRequiresTLSError
			if tc.refused != errors.As(err, &tlsErr) {
				t.Fatalf("err = %v, refused want %v", err, tc.refused)
			}
			if tc.refused && (tlsErr.Transport != "grpc" || tlsErr.Kind != BarrierSecretUnsealKey) {
				t.Fatalf("error = %+v, want the transport and the kind", tlsErr)
			}
		})
	}
}

// TestRequireTLSForBarrierSecret_SOPINKeepsItsError: a request's own SO PIN is
// refused with the SO PIN's error, as it is when bound by WithSOPIN.
func TestRequireTLSForBarrierSecret_SOPINKeepsItsError(t *testing.T) {
	err := RequireTLSForBarrierSecret("unix", false, &RekeyStartRequest{SOPin: testSecret})
	var soErr *SOPINRequiresTLSError
	if !errors.As(err, &soErr) || soErr.Transport != "unix" {
		t.Fatalf("err = %v, want *SOPINRequiresTLSError from unix", err)
	}
	var tlsErr *BarrierSecretRequiresTLSError
	if errors.As(err, &tlsErr) {
		t.Fatal("an SO PIN is reported as a barrier secret")
	}
}

func TestGuardBarrierSecret(t *testing.T) {
	ctx := context.Background()
	marked, err := GuardBarrierSecret(ctx, "rest", true, &TenantBarrierUnsealRequest{Key: []byte(testSecret)})
	if err != nil {
		t.Fatalf("GuardBarrierSecret over TLS: %v", err)
	}
	if kind, ok := barrierSecretInContext(marked); !ok || kind != BarrierSecretUnsealKey {
		t.Fatalf("context mark = %q, %v; want the unseal key", kind, ok)
	}

	unmarked, err := GuardBarrierSecret(ctx, "rest", false, &TenantBarrierUnsealRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("GuardBarrierSecret without a secret: %v", err)
	}
	if _, ok := barrierSecretInContext(unmarked); ok {
		t.Fatal("a request without a secret marks its context")
	}

	_, err = GuardBarrierSecret(ctx, "mcp", false, &TenantBarrierUnsealRequest{Key: []byte(testSecret)})
	var tlsErr *BarrierSecretRequiresTLSError
	if !errors.As(err, &tlsErr) || tlsErr.Transport != "mcp" {
		t.Fatalf("err = %v, want *BarrierSecretRequiresTLSError from mcp", err)
	}
}

func TestIsHTTPS(t *testing.T) {
	cases := map[string]bool{
		"https://db.example.com:8443":     true,
		"https://db.example.com/mcp":      true,
		"http://db.example.com:8080":      false,
		"db.example.com:8443":             false,
		"://bad":                          false,
		"":                                false,
		"HTTPS://db.example.com:8443/mcp": true,
	}
	for address, want := range cases {
		if got := IsHTTPS(address); got != want {
			t.Errorf("IsHTTPS(%q) = %v, want %v", address, got, want)
		}
	}
}

// TestSOPINRoundTripper_RefusesAPlaintextHopForABarrierSecret: a context
// GuardBarrierSecret marked is refused on an http:// hop, before it is sent,
// and sent on an https:// one.
func TestSOPINRoundTripper_RefusesAPlaintextHopForABarrierSecret(t *testing.T) {
	cases := []struct {
		name    string
		req     BarrierSecretCarrier
		url     string
		refused bool
		soPIN   bool
	}{
		{"barrier secret on http", &QuorumSubmitRequest{Share: []byte(testSecret)}, "http://db.example.com:8443/v1/barrier/quorum", true, false},
		{"SO PIN field on http", &SignCSRInitRequest{SOPin: testSecret}, "http://db.example.com:8443/v1/ceremony/sign-csr", true, true},
		{"barrier secret on https", &QuorumSubmitRequest{Share: []byte(testSecret)}, "https://db.example.com:8443/v1/barrier/quorum", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := GuardBarrierSecret(context.Background(), "rest", true, tc.req)
			if err != nil {
				t.Fatalf("GuardBarrierSecret: %v", err)
			}
			base := &captureRT{}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tc.url, nil)
			resp, err := SOPINRoundTripper(base, "rest", "db.example.com:8443").RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if !tc.refused {
				if err != nil || base.req == nil {
					t.Fatalf("https hop: err=%v sent=%v, want sent", err, base.req != nil)
				}
				return
			}
			if base.req != nil {
				t.Fatal("the refused request was sent")
			}
			var soErr *SOPINRequiresTLSError
			var tlsErr *BarrierSecretRequiresTLSError
			if tc.soPIN && !errors.As(err, &soErr) {
				t.Fatalf("err = %v, want *SOPINRequiresTLSError", err)
			}
			if !tc.soPIN && (!errors.As(err, &tlsErr) || tlsErr.Kind != BarrierSecretShare) {
				t.Fatalf("err = %v, want *BarrierSecretRequiresTLSError for the share", err)
			}
		})
	}
}

func TestBarrierSecretRequiresTLSError_NeverNamesTheSecret(t *testing.T) {
	kinds := []BarrierSecretKind{BarrierSecretInit, BarrierSecretUnsealKey, BarrierSecretPassphrase, BarrierSecretShare, BarrierSecretUserPIN}
	for _, kind := range kinds {
		err := RequireTLSForBarrierSecret("rest", false, kindCarrier(kind))
		msg := err.Error()
		if !strings.Contains(msg, string(kind)) || !strings.Contains(msg, "rest") {
			t.Fatalf("message %q must name the transport and the kind", msg)
		}
		if strings.Contains(msg, testSecret) {
			t.Fatalf("message %q names the secret", msg)
		}
	}
}

func TestBarrierSecretForeignHostError_NamesBothHostsNeverTheSecret(t *testing.T) {
	err := &BarrierSecretForeignHostError{Transport: "mcp", Kind: BarrierSecretShare, OriginalHost: "db.example.com:8443", RedirectHost: "other.example.com"}
	msg := err.Error()
	for _, want := range []string{"mcp", string(BarrierSecretShare), "db.example.com:8443", "other.example.com"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q must name %q", msg, want)
		}
	}
	if strings.Contains(msg, testSecret) {
		t.Fatalf("message %q names the secret", msg)
	}
}

// redirected returns a request for target whose redirect chain began at each
// of from in turn, the way an http.Client links the hops it follows.
func redirected(t *testing.T, target string, from ...string) *http.Request {
	t.Helper()
	var prev *http.Request
	for _, u := range append(from, target) {
		req, err := http.NewRequest(http.MethodPost, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		if prev != nil {
			req.Response = &http.Response{StatusCode: http.StatusTemporaryRedirect, Request: prev}
		}
		prev = req
	}
	return prev
}

func TestFirstHop(t *testing.T) {
	if req := redirected(t, "https://db.example.com/v1"); firstHop(req) != req {
		t.Fatal("a request that is not a redirect is its own first hop")
	}
	req := redirected(t, "https://c.example.com/v1", "https://a.example.com/v1", "https://b.example.com/v1")
	if got := firstHop(req).URL.Host; got != "a.example.com" {
		t.Fatalf("firstHop host = %q, want a.example.com", got)
	}
	// A hop whose redirect response has no request ends the chain there.
	orphan := redirected(t, "https://b.example.com/v1")
	orphan.Response = &http.Response{StatusCode: http.StatusPermanentRedirect}
	if firstHop(orphan) != orphan {
		t.Fatal("a redirect response without a request ends the chain")
	}
}

func TestBarrierSecretHopError(t *testing.T) {
	tests := []struct {
		name string
		kind BarrierSecretKind
		req  *http.Request
		want func(error) bool
	}{
		{"first hop https", BarrierSecretUnsealKey, redirected(t, "https://db.example.com/v1"), isNil},
		{"same-host redirect", BarrierSecretUnsealKey, redirected(t, "https://db.example.com/moved", "https://db.example.com/v1"), isNil},
		{"first hop http", BarrierSecretUnsealKey, redirected(t, "http://db.example.com/v1"), isAs[*BarrierSecretRequiresTLSError]},
		{"redirect to http", BarrierSecretShare, redirected(t, "http://db.example.com/v1", "https://db.example.com/v1"), isAs[*BarrierSecretRequiresTLSError]},
		{"redirect to another port", BarrierSecretPassphrase, redirected(t, "https://db.example.com:9443/v1", "https://db.example.com/v1"), isAs[*BarrierSecretForeignHostError]},
		{"redirect back after a foreign hop", BarrierSecretInit, redirected(t, "https://other.example.com/v1", "https://db.example.com/v1", "https://db.example.com/moved"), isAs[*BarrierSecretForeignHostError]},
		{"SO PIN to another host", soPINSecret, redirected(t, "https://other.example.com/v1", "https://db.example.com/v1"), isAs[*SOPINForeignHostError]},
		{"SO PIN to http", soPINSecret, redirected(t, "http://db.example.com/v1", "https://db.example.com/v1"), isAs[*SOPINRequiresTLSError]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := barrierSecretHopError("rest", tt.kind, tt.req); !tt.want(err) {
				t.Fatalf("barrierSecretHopError = %v", err)
			}
		})
	}

	err := barrierSecretHopError("mcp", BarrierSecretUserPIN, redirected(t, "https://other.example.com/v1", "https://db.example.com/v1"))
	want := BarrierSecretForeignHostError{Transport: "mcp", Kind: BarrierSecretUserPIN, OriginalHost: "db.example.com", RedirectHost: "other.example.com"}
	var hostErr *BarrierSecretForeignHostError
	if !errors.As(err, &hostErr) || *hostErr != want {
		t.Fatalf("err = %v, want %+v", err, want)
	}
}

func isNil(err error) bool { return err == nil }

func isAs[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// kindCarrier is a carrier of a fixed kind; it carries no value to leak.
type kindCarrier BarrierSecretKind

func (k kindCarrier) barrierSecret() BarrierSecretKind { return BarrierSecretKind(k) }
