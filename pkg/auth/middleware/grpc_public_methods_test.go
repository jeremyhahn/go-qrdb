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

package middleware

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// failingAuthenticator always fails. Tests use it to prove that a
// RegisterPublicGRPCMethods-declared method bypasses authentication itself,
// not merely permission lookup -- a distinct claim from isPublicGRPCMethod's
// pre-existing built-in exemption.
type failingAuthenticator struct{}

func (failingAuthenticator) Authenticate(context.Context) (*auth.Identity, error) {
	return nil, auth.ErrUnauthenticated
}

func (failingAuthenticator) Name() string { return "failing" }

// ---------------------------------------------------------------------------
// RegisterPublicGRPCMethods
// ---------------------------------------------------------------------------

// TestRegisterPublicGRPCMethods_ValidMethodsBypassAuthentication is the
// success case: a declared method reaches the handler even though the
// configured authenticator always fails, proving the bypass happens before
// authenticate runs, not merely before the permission lookup.
func TestRegisterPublicGRPCMethods_ValidMethodsBypassAuthentication(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	if err := m.RegisterPublicGRPCMethods([]string{"/quicbaas.v1.HealthService/Check"}); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}

	interceptor := m.UnaryInterceptor()
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/quicbaas.v1.HealthService/Check"}

	if _, err := interceptor(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("a registered public method must reach the handler without authentication")
	}
}

// TestRegisterPublicGRPCMethods_NonRegisteredMethodStillDenied covers the
// other half: a method that was never declared stays gated, even though a
// DIFFERENT method was registered as public.
func TestRegisterPublicGRPCMethods_NonRegisteredMethodStillDenied(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	if err := m.RegisterPublicGRPCMethods([]string{"/quicbaas.v1.HealthService/Check"}); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}

	interceptor := m.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/quicbaas.v1.HealthService/Version"}
	_, err := interceptor(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("a non-declared method must not reach the handler")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected the non-declared method to be denied")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want %v", got, codes.Unauthenticated)
	}
}

// TestRegisterPublicGRPCMethods_StreamBypassesAuthentication covers the
// streaming interceptor's mirror of the unary bypass.
func TestRegisterPublicGRPCMethods_StreamBypassesAuthentication(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	if err := m.RegisterPublicGRPCMethods([]string{"/quicbaas.v1.HealthService/Watch"}); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}

	interceptor := m.StreamInterceptor()
	called := false
	handler := func(srv any, stream grpc.ServerStream) error {
		called = true
		return nil
	}
	info := &grpc.StreamServerInfo{FullMethod: "/quicbaas.v1.HealthService/Watch"}

	if err := interceptor(nil, &boostServerStream{ctx: context.Background()}, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("a registered public stream method must reach the handler without authentication")
	}
}

// TestRegisterPublicGRPCMethods_StreamNonRegisteredMethodStillDenied is the
// streaming counterpart of the unary default-deny check.
func TestRegisterPublicGRPCMethods_StreamNonRegisteredMethodStillDenied(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	if err := m.RegisterPublicGRPCMethods([]string{"/quicbaas.v1.HealthService/Watch"}); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}

	interceptor := m.StreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/quicbaas.v1.HealthService/OtherStream"}
	err := interceptor(nil, &boostServerStream{ctx: context.Background()}, info, func(srv any, stream grpc.ServerStream) error {
		t.Fatal("a non-declared stream method must not reach the handler")
		return nil
	})
	if err == nil {
		t.Fatal("expected the non-declared stream method to be denied")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Errorf("code = %v, want %v", got, codes.Unauthenticated)
	}
}

// TestRegisterPublicGRPCMethods_MalformedNameRejected covers the typed
// validation error: a name that doesn't match the full gRPC method shape
// ("/package.Service/Method") must fail loudly at registration rather than
// silently leaving the intended RPC auth-gated.
func TestRegisterPublicGRPCMethods_MalformedNameRejected(t *testing.T) {
	tests := []struct {
		name   string
		method string
	}{
		{"missing leading slash", "quicbaas.v1.HealthService/Check"},
		{"no service separator", "/OnlyOneSegment"},
		{"empty method component", "/quicbaas.v1.HealthService/"},
		{"empty string", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(&mockAuthorizer{})

			err := m.RegisterPublicGRPCMethods([]string{tc.method})
			if err == nil {
				t.Fatalf("RegisterPublicGRPCMethods(%q) = nil, want an error", tc.method)
			}
			var want *InvalidPublicGRPCMethodError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *InvalidPublicGRPCMethodError", err, err)
			}
			if want.Method != tc.method {
				t.Errorf("Method = %q, want %q", want.Method, tc.method)
			}
		})
	}
}

// TestRegisterPublicGRPCMethods_MalformedEntryRejectsTheWholeBatch covers a
// batch with one bad name among good ones: none of the batch is admitted,
// so a single typo can never leave part of a module's intended set
// registered while silently dropping the rest.
func TestRegisterPublicGRPCMethods_MalformedEntryRejectsTheWholeBatch(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	err := m.RegisterPublicGRPCMethods([]string{
		"/quicbaas.v1.HealthService/Check",
		"not-a-full-method-name",
	})
	if err == nil {
		t.Fatal("expected an error for the malformed entry")
	}

	// The well-formed sibling in the same batch must NOT have been admitted.
	interceptor := m.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/quicbaas.v1.HealthService/Check"}
	_, callErr := interceptor(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("a method from a rejected batch must not have been registered")
		return nil, nil
	})
	if callErr == nil {
		t.Fatal("expected the method to remain gated")
	}
}

// TestRegisterPublicGRPCMethods_EmptyIsANoop covers the no-modules /
// no-declarations case: registering an empty (or nil) slice succeeds and
// registers nothing.
func TestRegisterPublicGRPCMethods_EmptyIsANoop(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	if err := m.RegisterPublicGRPCMethods(nil); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods(nil) = %v, want nil", err)
	}
	if err := m.RegisterPublicGRPCMethods([]string{}); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods([]string{}) = %v, want nil", err)
	}

	interceptor := m.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/quicbaas.v1.HealthService/Check"}
	_, err := interceptor(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		t.Fatal("nothing should have been registered as public")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected the method to remain gated")
	}
}

// TestRegisterPublicGRPCMethods_MultipleCallsAccumulate covers the
// "call more than once" contract documented on RegisterPublicGRPCMethods:
// e.g. the gRPC and unix transports sharing one AuthMiddleware instance,
// or two modules registering independently. The set only grows.
func TestRegisterPublicGRPCMethods_MultipleCallsAccumulate(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))

	if err := m.RegisterPublicGRPCMethods([]string{"/quicbaas.v1.HealthService/Check"}); err != nil {
		t.Fatalf("first RegisterPublicGRPCMethods: %v", err)
	}
	if err := m.RegisterPublicGRPCMethods([]string{"/quicbaas.v1.HealthService/Version"}); err != nil {
		t.Fatalf("second RegisterPublicGRPCMethods: %v", err)
	}

	interceptor := m.UnaryInterceptor()
	for _, method := range []string{
		"/quicbaas.v1.HealthService/Check",
		"/quicbaas.v1.HealthService/Version",
	} {
		info := &grpc.UnaryServerInfo{FullMethod: method}
		called := false
		_, err := interceptor(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
			called = true
			return "ok", nil
		})
		if err != nil {
			t.Errorf("method %q: unexpected error: %v", method, err)
		}
		if !called {
			t.Errorf("method %q: handler was not reached", method)
		}
	}
}

func TestInvalidPublicGRPCMethodError(t *testing.T) {
	err := &InvalidPublicGRPCMethodError{Method: "bad-name"}
	if err.Error() == "" {
		t.Fatal("Error() must not be empty")
	}
}
