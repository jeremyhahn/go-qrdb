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

package middleware

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

const grpcOBMethod = "/qrdb.v1.QRDBService/Put"

// grpcOnBehalfCtx returns a server-side context for a gRPC request from
// peerCert, carrying an assertion signKey signed for (method, signedReq).
func grpcOnBehalfCtx(t *testing.T, peerCert *x509.Certificate, signKey *ecdsa.PrivateKey, method string, signedReq proto.Message, keys ...string) context.Context {
	t.Helper()
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(signedReq)
	if err != nil {
		t.Fatal(err)
	}
	a := forward.RPCBinding("grpc", method, payload).Assertion(obAliceSubject, "acme", obNodeID, time.Now())
	ab, sb, err := forward.Sign(a, signKey)
	if err != nil {
		t.Fatal(err)
	}
	md := metadata.MD{}
	if len(keys) == 0 {
		keys = []string{forward.MetadataAssertion, forward.MetadataSignature}
	}
	for _, k := range keys {
		switch k {
		case forward.MetadataAssertion:
			md.Set(k, ab)
		case forward.MetadataSignature:
			md.Set(k, sb)
		}
	}
	ctx := metadata.NewIncomingContext(context.Background(), md)
	state := tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{peerCert}}}
	return peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{}, AuthInfo: credentials.TLSInfo{State: state}})
}

// runGRPCOnBehalf drives one request through the unary auth interceptor and
// returns whether the handler ran and the identity it saw.
func runGRPCOnBehalf(t *testing.T, ctx context.Context, req any, authn *mockAuthenticator, stored *user.User) (bool, *auth.Identity, error) {
	t.Helper()
	if authn == nil {
		authn = &mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}
	}
	if stored == nil {
		stored = &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{"kv-writer"}}
	}
	authz := &capturingAuthorizer{}
	// This file is about on-behalf forwarding, not auth.mtls_binding_policy
	// (go-qrdb#517/#548): the stored users here carry no enrolled
	// certificate binding, and under the default MTLSBindingRequired that
	// refuses every cert login for them, including the ordinary
	// (non-forwarded) chain some of these tests also exercise.
	// MTLSBindingEnrolled is the documented explicit opt-out for an
	// account policy was never meant to gate here.
	m := newTestMiddleware(authz, WithAuthenticator(authn), WithUserService(&mockUserServicer{user: stored}),
		WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	var ran bool
	var seen *auth.Identity
	_, err := m.UnaryInterceptor()(ctx, req, &grpc.UnaryServerInfo{FullMethod: grpcOBMethod},
		func(ctx context.Context, _ any) (any, error) {
			ran = true
			seen = auth.IdentityFromContext(ctx)
			return nil, nil
		})
	return ran, seen, err
}

func TestGRPCOnBehalf_ValidAuthenticatesAsTheCaller(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	req := wrapperspb.String("payload")
	ran, seen, err := runGRPCOnBehalf(t, grpcOnBehalfCtx(t, cert, key, grpcOBMethod, req), req, nil, nil)
	if err != nil || !ran {
		t.Fatalf("interceptor = %v (handler ran %v); want the forwarded request authenticated", err, ran)
	}
	if seen == nil || seen.Subject != obAliceSubject || seen.TenantID != "acme" || !isOnBehalfIdentity(seen) {
		t.Fatalf("identity = %+v; want the asserted caller, marked on-behalf", seen)
	}
}

// TestGRPCOnBehalf_ReleaseGapIsTypedRefusal proves a forward from a node two
// minors away is refused with the release gap on the replica-forwarding
// channel (rolling-upgrades clause (3)): FAILED_PRECONDITION naming both
// releases at the interceptor, a *release.GapError underneath, and the
// handler never runs.
func TestGRPCOnBehalf_ReleaseGapIsTypedRefusal(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	req := wrapperspb.String("payload")
	ctx := asRelease("0.61.0", func() context.Context { return grpcOnBehalfCtx(t, cert, key, grpcOBMethod, req) })
	t.Cleanup(release.SetForTesting("0.59.0"))

	ran, _, err := runGRPCOnBehalf(t, ctx, req, nil, nil)
	if ran {
		t.Fatal("handler ran for a forward across a release gap")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("interceptor = %v (code %s); want FailedPrecondition", err, status.Code(err))
	}
	for _, want := range []string{"replica-forwarding", `"0.59.0"`, `"0.61.0"`} {
		if !strings.Contains(status.Convert(err).Message(), want) {
			t.Fatalf("status message %q does not name %s", status.Convert(err).Message(), want)
		}
	}

	m := newTestMiddleware(&capturingAuthorizer{}, WithUserService(&mockUserServicer{}))
	_, _, aerr := m.authenticateGRPCOnBehalf(ctx, grpcOBMethod, req, mustMD(t, ctx, forward.MetadataAssertion), mustMD(t, ctx, forward.MetadataSignature))
	var gap *release.GapError
	if !errors.As(aerr, &gap) {
		t.Fatalf("authenticateGRPCOnBehalf = %v (%T); want *release.GapError", aerr, aerr)
	}
	if gap.Channel != "replica-forwarding" || gap.Local != "0.59.0" || gap.Peer != "0.61.0" {
		t.Fatalf("gap = %+v; want replica-forwarding, local 0.59.0, peer 0.61.0", gap)
	}
}

// TestGRPCOnBehalf_AdjacentReleaseAccepted proves a forward from the next or
// the previous minor release is served as before.
func TestGRPCOnBehalf_AdjacentReleaseAccepted(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	req := wrapperspb.String("payload")
	t.Cleanup(release.SetForTesting("0.59.0"))
	for _, sender := range []string{"0.60.0", "0.58.0"} {
		ctx := asRelease(sender, func() context.Context { return grpcOnBehalfCtx(t, cert, key, grpcOBMethod, req) })
		ran, seen, err := runGRPCOnBehalf(t, ctx, req, nil, nil)
		if err != nil || !ran || seen == nil || seen.Subject != obAliceSubject {
			t.Fatalf("sender %s: interceptor = %v (handler ran %v, identity %+v); want served", sender, err, ran, seen)
		}
	}
}

// mustMD returns the first value of key in ctx's incoming metadata.
func mustMD(t *testing.T, ctx context.Context, key string) string {
	t.Helper()
	v := metadata.ValueFromIncomingContext(ctx, key)
	if len(v) == 0 {
		t.Fatalf("metadata %s missing", key)
	}
	return v[0]
}

func TestGRPCOnBehalf_RefusesEveryTamperAndANonClusterPeer(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	userCert, userKey := obUserCert(t)
	req := wrapperspb.String("payload")
	for name, ctx := range map[string]context.Context{
		"payload swapped":  grpcOnBehalfCtx(t, cert, key, grpcOBMethod, wrapperspb.String("other")),
		"method swapped":   grpcOnBehalfCtx(t, cert, key, "/qrdb.v1.QRDBService/Delete", req),
		"non-cluster peer": grpcOnBehalfCtx(t, userCert, userKey, grpcOBMethod, req),
	} {
		ran, _, err := runGRPCOnBehalf(t, ctx, req, nil, nil)
		if err == nil || ran {
			t.Errorf("%s: interceptor = %v (handler ran %v); want refused", name, err, ran)
		}
	}
	if ran, _, err := runGRPCOnBehalf(t, grpcOnBehalfCtx(t, cert, key, grpcOBMethod, req), "not a proto message", nil, nil); err == nil || ran {
		t.Errorf("a non-proto request was authenticated on-behalf: %v", err)
	}
}

// TestGRPCOnBehalf_HalfAnAssertionIsNotForwarded: a request carrying only one
// of the two keys authenticates as itself, through the ordinary chain.
func TestGRPCOnBehalf_HalfAnAssertionIsNotForwarded(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	req := wrapperspb.String("payload")
	direct := &mockAuthenticator{name: "mtls", identity: &auth.Identity{Subject: "bob", TenantID: "acme"}}
	ran, seen, err := runGRPCOnBehalf(t, grpcOnBehalfCtx(t, cert, key, grpcOBMethod, req, forward.MetadataAssertion), req, direct,
		&user.User{ID: 4200, Username: "bob", TenantID: "acme", Lifecycle: user.LifecycleActive})
	if err != nil || !ran || seen == nil || seen.Subject != auth.UserSubject(4200) || seen.Username != "bob" || isOnBehalfIdentity(seen) {
		t.Fatalf("interceptor = %v, identity %+v; want the ordinary authenticator's caller", err, seen)
	}
}
