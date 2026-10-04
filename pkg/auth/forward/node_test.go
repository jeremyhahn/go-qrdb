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

package forward

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestSignCaller_AssertsTheCallerAndKeepsAHolderAHolder: the signed assertion
// verifies for the binding and names the caller; a holder's carries its
// profile.
func TestSignCaller_AssertsTheCallerAndKeepsAHolderAHolder(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	b := RPCBinding("grpc", "/qrdb.v1.QRDBService/Put", []byte("payload"))
	for name, tc := range map[string]struct {
		caller  *auth.Identity
		profile string
	}{
		"user":   {caller: &auth.Identity{Subject: "alice", TenantID: "acme"}},
		"holder": {caller: &auth.Identity{Subject: "c1", TenantID: "acme", Metadata: map[string]string{auth.HolderProfileMetadataKey: "custodian"}}, profile: "custodian"},
	} {
		ab, sb, err := SignCaller(tc.caller, b, key, nodeID, now)
		if err != nil {
			t.Fatalf("%s: SignCaller = %v", name, err)
		}
		got, err := VerifyBinding(ab, sb, cert, tdom, now, b)
		if err != nil || got.Subject != tc.caller.Subject || got.TenantID != "acme" || got.HolderProfile != tc.profile {
			t.Errorf("%s: verified %+v, %v; want %s/acme profile %q", name, got, err, tc.caller.Subject, tc.profile)
		}
	}
}

// TestSignCaller_RefusesToSignForNobodyOrAsNobody: no caller, or no node
// identity, is a typed refusal, never an assertion.
func TestSignCaller_RefusesToSignForNobodyOrAsNobody(t *testing.T) {
	_, key := nodeCert(t, tdom, nodeID)
	b := RPCBinding("grpc", "/m", nil)
	for name, call := range map[string]func() (string, string, error){
		"nil caller":    func() (string, string, error) { return SignCaller(nil, b, key, nodeID, time.Now()) },
		"empty subject": func() (string, string, error) { return SignCaller(&auth.Identity{}, b, key, nodeID, time.Now()) },
		"no key": func() (string, string, error) {
			return SignCaller(&auth.Identity{Subject: "a"}, b, nil, nodeID, time.Now())
		},
		"no node id": func() (string, string, error) {
			return SignCaller(&auth.Identity{Subject: "a"}, b, key, "", time.Now())
		},
	} {
		ab, sb, err := call()
		var unsigned *ForwardUnsignedError
		if !errors.As(err, &unsigned) || ab != "" || sb != "" || unsigned.Error() == "" {
			t.Errorf("%s: SignCaller = %q, %q, %v; want *ForwardUnsignedError", name, ab, sb, err)
		}
	}
}

// TestNodeIdentity_ReadsTheNodeCertificate: the signer and SPIFFE node id
// come from the static certificate or the client-certificate callback.
func TestNodeIdentity_ReadsTheNodeCertificate(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	tc := tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key}
	for name, cfg := range map[string]*tls.Config{
		"static":   {Certificates: []tls.Certificate{tc}},
		"callback": {GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &tc, nil }},
	} {
		if s, id := NodeIdentity(cfg); s == nil || id != nodeID {
			t.Errorf("%s: NodeIdentity = %v, %q; want the node key and %q", name, s, id, nodeID)
		}
	}
	for name, cfg := range map[string]*tls.Config{
		"no certificate":   {},
		"no key":           {Certificates: []tls.Certificate{{Certificate: [][]byte{cert.Raw}}}},
		"unparseable leaf": {Certificates: []tls.Certificate{{Certificate: [][]byte{[]byte("junk")}, PrivateKey: key}}},
		"empty chain":      {Certificates: []tls.Certificate{{PrivateKey: key}}},
	} {
		if s, id := NodeIdentity(cfg); s != nil || id != "" {
			t.Errorf("%s: NodeIdentity = %v, %q; want nothing", name, s, id)
		}
	}
}
