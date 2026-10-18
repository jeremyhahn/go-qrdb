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

// No build constraint: generateCredentialE2ETLS and credentialE2ERoundTrip
// (handlers_credentials_e2e_test.go) are unconstrained.

package quic

import (
	"context"
	"crypto/tls"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// quicDispatchAuthenticator refuses every caller and counts how often it was
// asked: a request it sees is a request that reached authentication, and so
// the user store behind it.
type quicDispatchAuthenticator struct{ calls atomic.Int64 }

func (a *quicDispatchAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	a.calls.Add(1)
	return nil, auth.ErrUnauthenticated
}

func (a *quicDispatchAuthenticator) Name() string { return "counting" }

// quicDispatchBarrier serves barrier.Unseal and records whether the call
// reached it anonymously; every other barrier method is unimplemented.
type quicDispatchBarrier struct {
	service.BarrierServicer
	anonymous atomic.Bool
}

func (b *quicDispatchBarrier) BarrierUnseal(ctx context.Context, _ []byte) error {
	b.anonymous.Store(sealedwindow.Anonymous(ctx))
	return nil
}

func (b *quicDispatchBarrier) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	b.anonymous.Store(sealedwindow.Anonymous(ctx))
	return &service.BarrierStatusInfo{Sealed: true, Initialized: true, ClusterSeal: &clusterseal.Report{}}, nil
}

// TestSealedWindowDispatch_QUIC: a regular QUIC listener serves a sealed
// node exactly as the boot-time listener does -- the sealed-window methods
// anonymously, everything else refused Unavailable, neither authenticated --
// and authenticates every request once the node barrier is unsealed, a
// cluster-held node included. Client certificates stay required at the
// handshake throughout.
func TestSealedWindowDispatch_QUIC(t *testing.T) {
	tests := []struct {
		name                string
		nodeSealed, barrier bool
		method              string
		want                uint16
		authentications     int64
	}{
		{name: "sealed, unseal", nodeSealed: true, barrier: true, method: "barrier.Unseal", want: wire.StatusOK},
		{name: "sealed, data method", nodeSealed: true, barrier: true, method: "kv.Get", want: wire.StatusUnavailable},
		{name: "sealed, barrier status", nodeSealed: true, barrier: true, method: "barrier.Status", want: wire.StatusOK},
		{name: "cluster-held, barrier status", nodeSealed: true, method: "barrier.Status", want: wire.StatusUnauthenticated, authentications: 1},
		{name: "sealed, health", nodeSealed: true, barrier: true, method: "admin.Health", want: wire.StatusUnavailable},
		{name: "sealed, authenticated-only method", nodeSealed: true, barrier: true, method: "barrier.ClusterUnseal", want: wire.StatusUnavailable},
		{name: "unsealed", method: "barrier.Unseal", want: wire.StatusUnauthenticated, authentications: 1},
		{name: "cluster-held", nodeSealed: true, method: "barrier.ClusterUnseal", want: wire.StatusUnauthenticated, authentications: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authn := &quicDispatchAuthenticator{}
			barrier := &quicDispatchBarrier{}
			serverCfg, clientCfg := generateCredentialE2ETLS(t)
			serverCfg.ClientAuth = tls.RequireAnyClientCert
			clientCfg.Certificates = serverCfg.Certificates
			srv := NewServer(nil, nil, "127.0.0.1:0", WithTLS(serverCfg),
				WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
				WithBarrierService(barrier),
				WithSealedWindowProbe(&fakeSealedProbe{sealed: tt.nodeSealed}),
				WithSealedWindowDispatch(&fakeSealedProbe{sealed: tt.barrier}))
			if err := srv.Start(); err != nil {
				t.Fatalf("start server: %v", err)
			}
			t.Cleanup(srv.Stop)

			status, payload := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, tt.method, nil)
			if status != tt.want {
				t.Fatalf("status = %s, want %s", wire.StatusName(status), wire.StatusName(tt.want))
			}
			if tt.method == "barrier.Status" && status == wire.StatusOK {
				var resp pb.BarrierStatusResponse
				if err := proto.Unmarshal(payload, &resp); err != nil || !resp.GetSealed() || !resp.GetInitialized() {
					t.Fatalf("barrier.Status = (%v, %v), want sealed and initialized", &resp, err)
				}
			}
			if got, want := barrier.anonymous.Load(), tt.want == wire.StatusOK; got != want {
				t.Fatalf("served anonymously = %v, want %v", got, want)
			}
			if n := authn.calls.Load(); n != tt.authentications {
				t.Fatalf("authentications = %d, want %d", n, tt.authentications)
			}
		})
	}
}
