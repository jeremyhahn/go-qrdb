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

package etcd

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ctxKey marks a context as having passed the authorizer.
type ctxKey struct{}

// recordingAuthorizer records the permission it was asked for and answers
// with err, marking the returned context.
type recordingAuthorizer struct {
	asked []permission.Permission
	err   error
}

func (a *recordingAuthorizer) AuthorizeRequest(ctx context.Context, perm permission.Permission) (context.Context, error) {
	a.asked = append(a.asked, perm)
	if a.err != nil {
		return nil, a.err
	}
	return context.WithValue(ctx, ctxKey{}, true), nil
}

// newStartable builds a Server that Start accepts: TLS and an authorizer
// that admits every request, ahead of opts so a test's own option wins.
func newStartable(db, addr string, opts ...ServerOption) *Server {
	base := []ServerOption{
		WithTLS(&tls.Config{MinVersion: tls.VersionTLS13}),
		WithRequestAuthorizer(&recordingAuthorizer{}),
	}
	return NewServer(db, addr, append(base, opts...)...)
}

// The etcd Auth service is a stub, so a listener without TLS or without an
// authorizer would serve its database to anyone who reaches the port.
func TestStart_RefusesWithoutAuthentication(t *testing.T) {
	cases := map[string][]ServerOption{
		"no TLS":        {WithRequestAuthorizer(&recordingAuthorizer{})},
		"no authorizer": {WithTLS(&tls.Config{MinVersion: tls.VersionTLS13})},
		"neither":       nil,
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			s := NewServer("vault", "127.0.0.1:0", opts...)
			err := s.Start()
			var required *AuthRequiredError
			if !errors.As(err, &required) {
				s.Stop()
				t.Fatalf("Start() = %v, want *AuthRequiredError", err)
			}
			if required.Database != "vault" || s.listener != nil {
				t.Errorf("refusal = %+v, listener = %v; want database vault and no listener", required, s.listener)
			}
		})
	}
}

func TestMethodPermission(t *testing.T) {
	if perm, ok := MethodPermission("/etcdserverpb.KV/Put"); !ok || perm != permission.KVPut {
		t.Errorf("KV/Put = %q, %v; want %q", perm, ok, permission.KVPut)
	}
	for _, denied := range []string{"/etcdserverpb.Auth/Authenticate", "/etcdserverpb.Maintenance/Snapshot", "/etcdserverpb.Cluster/MemberAdd"} {
		if _, ok := MethodPermission(denied); ok {
			t.Errorf("%s is served; want it refused", denied)
		}
	}
}

func TestUnaryAuthorize(t *testing.T) {
	handled := false
	handler := func(ctx context.Context, _ any) (any, error) {
		handled = ctx.Value(ctxKey{}) == true
		return "ok", nil
	}

	authz := &recordingAuthorizer{}
	s := NewServer("db", ":0", WithRequestAuthorizer(authz))
	if _, err := s.unaryAuthorize(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.KV/Range"}, handler); err != nil {
		t.Fatalf("authorized Range: %v", err)
	}
	if !handled || len(authz.asked) != 1 || authz.asked[0] != permission.KVRange {
		t.Fatalf("handled = %v, asked = %v; want the handler to run with the authorized context after asking for %q", handled, authz.asked, permission.KVRange)
	}

	// An unserved method never reaches the authorizer or the handler.
	handled = false
	_, err := s.unaryAuthorize(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.Auth/UserAdd"}, handler)
	if status.Code(err) != codes.PermissionDenied || handled || len(authz.asked) != 1 {
		t.Fatalf("unserved method: code %v, handled %v, asked %v; want PermissionDenied before authorization", status.Code(err), handled, authz.asked)
	}

	// A status error from the authorizer passes through; any other error is
	// a denial.
	authz.err = status.Error(codes.Unauthenticated, "who are you")
	if _, err := s.unaryAuthorize(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.KV/Put"}, handler); status.Code(err) != codes.Unauthenticated {
		t.Errorf("status error: code %v, want Unauthenticated", status.Code(err))
	}
	authz.err = errors.New("plain refusal")
	if _, err := s.unaryAuthorize(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/etcdserverpb.KV/Put"}, handler); status.Code(err) != codes.PermissionDenied {
		t.Errorf("plain error: code %v, want PermissionDenied", status.Code(err))
	}
	if handled {
		t.Error("the handler ran for a refused request")
	}
}

// fakeStream is a grpc.ServerStream whose context is the one it was built
// with.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context     { return f.ctx }
func (f *fakeStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeStream) SetTrailer(metadata.MD)       {}

func TestStreamAuthorize(t *testing.T) {
	authz := &recordingAuthorizer{}
	s := NewServer("db", ":0", WithRequestAuthorizer(authz))
	info := &grpc.StreamServerInfo{FullMethod: "/etcdserverpb.Watch/Watch", IsServerStream: true, IsClientStream: true}

	var seen bool
	handler := func(_ any, ss grpc.ServerStream) error {
		seen = ss.Context().Value(ctxKey{}) == true
		return nil
	}
	if err := s.streamAuthorize(nil, &fakeStream{ctx: context.Background()}, info, handler); err != nil {
		t.Fatalf("authorized Watch: %v", err)
	}
	if !seen || authz.asked[0] != permission.WatchWatchPrefix {
		t.Fatalf("stream context authorized = %v, asked %v", seen, authz.asked)
	}

	authz.err = status.Error(codes.Unavailable, "sealed")
	seen = false
	if err := s.streamAuthorize(nil, &fakeStream{ctx: context.Background()}, info, handler); status.Code(err) != codes.Unavailable || seen {
		t.Fatalf("refused stream: code %v, handler ran %v", status.Code(err), seen)
	}
}

func TestAuthorizationErrors_Error(t *testing.T) {
	cases := []struct {
		err  error
		want []string
	}{
		{&AuthRequiredError{Database: "vault"}, []string{"vault", "authentication"}},
		{&ScopeUnavailableError{Database: "vault", Reason: "no catalog"}, []string{"vault", "no catalog"}},
		{&MethodNotAllowedError{Method: "/x/Y"}, []string{"/x/Y"}},
		{&TenantMismatchError{CallerTenant: "evil", OwnerTenant: "acme", Database: "app"}, []string{"evil", "acme", "app"}},
		{&PermissionDeniedError{}, []string{"etcd", "permission denied"}},
	}
	for _, c := range cases {
		msg := c.err.Error()
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%T.Error() = %q, want %q", c.err, msg, w)
			}
		}
	}
}
