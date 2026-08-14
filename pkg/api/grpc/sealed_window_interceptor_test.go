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

package grpc

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeSealProbe struct{ sealed atomic.Bool }

func (f *fakeSealProbe) IsSealed() bool { return f.sealed.Load() }
func (f *fakeSealProbe) seal()          { f.sealed.Store(true) }
func (f *fakeSealProbe) unseal()        { f.sealed.Store(false) }

func newProbe(sealed bool) *fakeSealProbe {
	p := &fakeSealProbe{}
	if sealed {
		p.seal()
	}
	return p
}

var dummyHandler grpc.UnaryHandler = func(ctx context.Context, req any) (any, error) {
	return "ok", nil
}

var dummyStreamHandler grpc.StreamHandler = func(srv any, ss grpc.ServerStream) error {
	return nil
}

type fakeServerStream struct{ grpc.ServerStream }

func TestSealedWindow_Unary_NilProbeIsPassthrough(t *testing.T) {
	intercept := SealedWindowUnaryInterceptor(nil, nil)
	res, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/KVGet"},
		dummyHandler)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if res != "ok" {
		t.Errorf("res = %v, want ok", res)
	}
}

func TestSealedWindow_Unary_UnsealedIsPassthrough(t *testing.T) {
	intercept := SealedWindowUnaryInterceptor(newProbe(false), nil)
	res, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/KVGet"},
		dummyHandler)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if res != "ok" {
		t.Errorf("res = %v, want ok", res)
	}
}

func TestSealedWindow_Unary_SealedNonAllowlistRejected(t *testing.T) {
	intercept := SealedWindowUnaryInterceptor(newProbe(true), nil)
	for _, method := range []string{
		"/qrdb.v1.QRDBService/KVGet",
		"/qrdb.v1.QRDBService/KVPut",
		"/qrdb.v1.QRDBService/VectorSearch",
		"/qrdb.v1.QRDBService/UserCreate",
	} {
		t.Run(method, func(t *testing.T) {
			_, err := intercept(context.Background(), nil,
				&grpc.UnaryServerInfo{FullMethod: method}, dummyHandler)
			if err == nil {
				t.Fatalf("expected error for %s", method)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("err is not a status: %v", err)
			}
			if st.Code() != codes.Unavailable {
				t.Errorf("code = %v, want Unavailable", st.Code())
			}
			if st.Message() != "barrier sealed" {
				t.Errorf("message = %q, want barrier sealed", st.Message())
			}
		})
	}
}

func TestSealedWindow_Unary_SealedAllowlistPasses(t *testing.T) {
	intercept := SealedWindowUnaryInterceptor(newProbe(true), nil)
	for _, full := range SealedWindowAllowlist {
		t.Run(full, func(t *testing.T) {
			res, err := intercept(context.Background(), nil,
				&grpc.UnaryServerInfo{FullMethod: full}, dummyHandler)
			if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if res != "ok" {
				t.Errorf("res = %v, want ok", res)
			}
		})
	}
}

// TestSealedWindow_Unary_SameTailOtherServiceRejected is the L4 regression
// guard: a method whose bare tail matches an allowlisted QRDBService verb but
// which belongs to a DIFFERENT co-registered gRPC service must be rejected
// while the barrier is sealed. Before the full-method match, tail-matching
// admitted "/qrdb.v1.TenantBarrierService/Status" through the sealed window
// because its tail "Status" collided with QRDBService/Status.
func TestSealedWindow_Unary_SameTailOtherServiceRejected(t *testing.T) {
	intercept := SealedWindowUnaryInterceptor(newProbe(true), nil)
	for _, method := range []string{
		"/qrdb.v1.TenantBarrierService/Status",
		"/qrdb.v1.SomeOtherService/Health",
		"/qrdb.v1.TenantBarrierService/Unseal",
	} {
		t.Run(method, func(t *testing.T) {
			_, err := intercept(context.Background(), nil,
				&grpc.UnaryServerInfo{FullMethod: method}, dummyHandler)
			if err == nil {
				t.Fatalf("expected %s to be rejected while sealed", method)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("err is not a status: %v", err)
			}
			if st.Code() != codes.Unavailable {
				t.Errorf("code = %v, want Unavailable", st.Code())
			}
		})
	}

	// The real QRDBService/Status still passes while sealed.
	if _, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Status"},
		dummyHandler); err != nil {
		t.Errorf("QRDBService/Status err = %v, want nil", err)
	}
}

func TestSealedWindow_Unary_DynamicSealUnseal(t *testing.T) {
	probe := newProbe(false)
	intercept := SealedWindowUnaryInterceptor(probe, nil)
	send := func() error {
		_, err := intercept(context.Background(), nil,
			&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/KVGet"},
			dummyHandler)
		return err
	}
	if err := send(); err != nil {
		t.Errorf("initial err = %v, want nil", err)
	}
	probe.seal()
	if err := send(); err == nil {
		t.Error("expected error after seal")
	}
	probe.unseal()
	if err := send(); err != nil {
		t.Errorf("after unseal err = %v, want nil", err)
	}
}

func TestSealedWindow_Unary_CustomAllowlist(t *testing.T) {
	intercept := SealedWindowUnaryInterceptor(newProbe(true),
		[]string{"/qrdb.v1.QRDBService/OnlyThis"})
	if _, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/OnlyThis"},
		dummyHandler); err != nil {
		t.Errorf("OnlyThis err = %v, want nil", err)
	}
	if _, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/Health"},
		dummyHandler); err == nil {
		t.Error("Health expected to be rejected with custom allowlist")
	}
	// The same bare tail on a different service must NOT be admitted by the
	// custom (full-path) allowlist.
	if _, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.OtherService/OnlyThis"},
		dummyHandler); err == nil {
		t.Error("OtherService/OnlyThis expected to be rejected with full-path allowlist")
	}
}

func TestSealedWindow_Stream_NilProbeIsPassthrough(t *testing.T) {
	intercept := SealedWindowStreamInterceptor(nil, nil)
	if err := intercept(nil, &fakeServerStream{},
		&grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"},
		dummyStreamHandler); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestSealedWindow_Stream_SealedNonAllowlistRejected(t *testing.T) {
	intercept := SealedWindowStreamInterceptor(newProbe(true), nil)
	err := intercept(nil, &fakeServerStream{},
		&grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"},
		dummyStreamHandler)
	if err == nil {
		t.Fatal("expected error for sealed non-allowlist stream")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", st.Code())
	}
}

func TestSealedWindow_Stream_SealedAllowlistPasses(t *testing.T) {
	intercept := SealedWindowStreamInterceptor(newProbe(true), nil)
	if err := intercept(nil, &fakeServerStream{},
		&grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/CeremonyStatus"},
		dummyStreamHandler); err != nil {
		t.Errorf("allowlist stream err = %v, want nil", err)
	}
}

func TestMethodAllowed_FullMatch(t *testing.T) {
	allowSet := map[string]struct{}{"/svc.v1.Service/Foo": {}}
	cases := []struct {
		full string
		want bool
	}{
		{"/svc.v1.Service/Foo", true},
		{"/svc.v1.Other/Foo", false}, // same tail, different service
		{"/svc/Foo", false},          // different service path
		{"Foo", false},               // bare tail no longer matches
		{"/svc.v1.Service/Bar", false},
		{"/svc.v1.Service/foo", false}, // case-sensitive
		{"", false},
	}
	for _, c := range cases {
		if got := methodAllowed(c.full, allowSet); got != c.want {
			t.Errorf("methodAllowed(%q) = %v, want %v", c.full, got, c.want)
		}
	}
}
