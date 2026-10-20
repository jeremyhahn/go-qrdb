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

package embedded

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fwdGrant is the delegation the forwarding tests present in-process.
var fwdGrant = delegation.Grant{Tenant: "acme", Principal: "embedded-fwd-test", Scope: []string{string(permission.KVPut)}}

// fwdRevoked answers that every certificate is revoked.
type fwdRevoked struct{}

func (fwdRevoked) IsCertRevoked(context.Context, *x509.Certificate) (bool, error) { return true, nil }

// received is what the receiving node makes of a forwarded caller: the
// forwarder carries it into the assertion it signs (forward.SignCaller), and
// the receiver decodes the carried delegation and verifies it with its own
// mTLS authenticator, as the middleware's on-behalf path does.
func received(t *testing.T, v *auth.MTLSAuthenticator, method string, payload []byte, caller *auth.Identity) (*auth.Identity, error) {
	t.Helper()
	a := forward.RPCBinding("quic", method, payload).Assertion(caller.Subject, caller.TenantID, "node-2", time.Now())
	a.Carry(caller)
	if a.Delegation == "" {
		t.Fatalf("forwarded as %+v, which carries no delegation", caller)
	}
	der, err := base64.StdEncoding.DecodeString(a.Delegation)
	if err != nil {
		t.Fatalf("carried delegation: %v", err)
	}
	return v.VerifyDelegation(context.Background(), der)
}

// TestForwarding_ADelegatedContextIsForwardedAsTheDelegatedCaller: a call on
// a context carrying a delegation leaves on every forwarding path -- to a
// replica, to a lease leader, to the admin-credential leader -- as the
// delegated caller, not the node's configured embedded caller, so the
// receiving node re-verifies the delegation and refuses it once revoked.
func TestForwarding_ADelegatedContextIsForwardedAsTheDelegatedCaller(t *testing.T) {
	d := authtest.NewDelegation(t, fwdGrant)
	delegated := d.Identity(t)
	ctx := auth.WithIdentity(acmeCtx(), delegated)
	lease, err := proto.Marshal(&pb.LeaseTimeToLiveResponse{Lease: &pb.Lease{Id: 7, Ttl: 10, RemainingTtl: 9}})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := proto.Marshal(&pb.CeremonyInitializeResponse{SpkiPin: "leader-answered"})
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		body []byte
		opts []Option
		call func(*Transport) error
		sent func(*recordingForwarder) (method string, payload []byte, caller *auth.Identity, ok bool)
	}{
		"to a replica": {
			opts: []Option{WithServiceRegistry(&nonReplicaRegistry{})},
			call: func(tr *Transport) error {
				scoped, err := tr.WithDatabase("orders")
				if err != nil {
					return err
				}
				return scoped.(transport.KVClient).Put(ctx, "k1", []byte("v1"))
			},
			sent: func(f *recordingForwarder) (string, []byte, *auth.Identity, bool) {
				if len(f.calls) != 1 {
					return "", nil, nil, false
				}
				return f.calls[0].method, f.calls[0].payload, f.calls[0].caller, true
			},
		},
		"to the lease leader": {
			body: lease,
			opts: []Option{WithLeaseService(&mockLeaseService{}), WithServiceRegistry(&followerLeaseRegistry{leaderAddr: "192.0.2.2:7000"})},
			call: func(tr *Transport) error {
				scoped, err := tr.WithDatabase("orders")
				if err != nil {
					return err
				}
				_, err = scoped.(transport.LeaseClient).LeaseTimeToLive(ctx, 7)
				return err
			},
			sent: leaderSent,
		},
		"to the admin-credential leader": {
			body: pin,
			opts: []Option{WithCeremonyService(&mockCeremonyService{initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
				return nil, &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}
			}})},
			call: func(tr *Transport) error {
				_, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{})
				return err
			},
			sent: leaderSent,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fwd := &recordingForwarder{status: wire.StatusOK, body: tc.body}
			tr, err := New(newMockKVService(), &mockAdminService{}, append(tc.opts, WithReplicaForwarding(fwd, fixedCaller{id: alice}))...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := tc.call(tr); err != nil {
				t.Fatalf("call = %v; want the receiving node's answer", err)
			}
			method, payload, caller, ok := tc.sent(fwd)
			if !ok {
				t.Fatalf("forwarded %+v / %+v; want exactly one frame", fwd.calls, fwd.leaderCalls)
			}
			if caller != delegated {
				t.Fatalf("forwarded as %+v; want the delegated caller, not the embedded caller", caller)
			}

			got, err := received(t, d.Authenticator, method, payload, caller)
			if err != nil || got.Subject != delegated.Subject || got.TenantID != "acme" {
				t.Fatalf("receiving node = %+v, %v; want the delegated principal in acme", got, err)
			}
			got, err = received(t, d.NewAuthenticator(auth.WithRevocationChecker(fwdRevoked{})), method, payload, caller)
			if got != nil || !errors.Is(err, auth.ErrCertificateRevoked) {
				t.Fatalf("receiving node, delegation revoked = %+v, %v; want auth.ErrCertificateRevoked", got, err)
			}
		})
	}
}

// TestForwarding_ADelegationOutsideTheRefusedTenantIsNotForwarded: the
// delegated caller is held to the same tenant check the configured caller
// is, so a delegation for another tenant never leaves the node.
func TestForwarding_ADelegationOutsideTheRefusedTenantIsNotForwarded(t *testing.T) {
	globex := authtest.NewDelegation(t, delegation.Grant{Tenant: "globex", Principal: fwdGrant.Principal, Scope: fwdGrant.Scope}).Identity(t)
	fwd := &recordingForwarder{status: wire.StatusOK}
	kv := forwardingTransport(t, &nonReplicaRegistry{}, fwd, fixedCaller{id: alice}).(transport.KVClient)
	if err := kv.Put(auth.WithIdentity(acmeCtx(), globex), "k1", []byte("v1")); !isRefusal(err) {
		t.Fatalf("Put = %v; want the node's own refusal", err)
	}
	if len(fwd.calls) != 0 {
		t.Fatalf("forwarded %+v for a globex delegation on an acme database; want none", fwd.calls)
	}
}

// leaderSent reports the one frame a forwarder sent to a leader.
func leaderSent(f *recordingForwarder) (string, []byte, *auth.Identity, bool) {
	if len(f.leaderCalls) != 1 {
		return "", nil, nil, false
	}
	return f.leaderCalls[0].method, f.leaderCalls[0].payload, f.leaderCalls[0].caller, true
}
