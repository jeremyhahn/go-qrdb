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
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

type fakeSealProbe struct {
	sealed, enrollment atomic.Bool
	sealedwindow.Admission
}

func (f *fakeSealProbe) IsSealed() bool                      { return f.sealed.Load() }
func (f *fakeSealProbe) EnrollmentOpen(context.Context) bool { return f.enrollment.Load() }
func (f *fakeSealProbe) seal()                               { f.sealed.Store(true) }
func (f *fakeSealProbe) unseal()                             { f.sealed.Store(false) }

func newProbe(sealed bool) *fakeSealProbe {
	p := &fakeSealProbe{}
	if sealed {
		p.seal()
	}
	return p
}

// assertSealedRefusal fails unless err is the sealed refusal: Unavailable
// with the sealed message and a google.rpc.ErrorInfo in the qrdb domain with
// reason SEALED, which is what lets a client tell it from a draining node.
func assertSealedRefusal(t *testing.T, what string, err error) {
	t.Helper()
	st, _ := status.FromError(err)
	if st.Code() != codes.Unavailable || st.Message() != sealedwindow.SealedMessage {
		t.Errorf("%s: %v, want Unavailable %q", what, err, sealedwindow.SealedMessage)
		return
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok &&
			info.GetDomain() == sealedwindow.ErrorDomain && info.GetReason() == sealedwindow.ReasonSealed {
			return
		}
	}
	t.Errorf("%s: %v carries no ErrorInfo{%s, %s}", what, err, sealedwindow.ErrorDomain, sealedwindow.ReasonSealed)
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

// stateProbe reports the two-valued seal state of sealedwindow.StateProbe.
type stateProbe struct{ sealed, unconfirmed bool }

func (p stateProbe) IsSealed() bool                        { return p.sealed }
func (p stateProbe) SealState() (sealed, unconfirmed bool) { return p.sealed, p.unconfirmed }

var _ sealedwindow.StateProbe = stateProbe{}

func TestSealedWindow_Unary_UnconfirmedIsNotSealed(t *testing.T) {
	// An unconfirmed node is not sealed, so it refuses as unconfirmed; like a
	// held node it answers no status question, barrier status included.
	for _, method := range []string{"/qrdb.v1.QRDBService/KVGet", "/qrdb.v1.QRDBService/BarrierStatus"} {
		info := &grpc.UnaryServerInfo{FullMethod: method}
		_, err := SealedWindowUnaryInterceptor(stateProbe{sealed: true, unconfirmed: true}, nil)(context.Background(), nil, info, dummyHandler)
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.Unavailable {
			t.Fatalf("%s: err = %v, want Unavailable", method, err)
		}
		if want := (&clusterseal.SealStateUnconfirmedError{}).Error(); st.Message() != want {
			t.Errorf("%s: message = %q, want %q", method, st.Message(), want)
		}
		var reason, domain string
		for _, d := range st.Details() {
			if ei, isInfo := d.(*errdetails.ErrorInfo); isInfo {
				reason, domain = ei.GetReason(), ei.GetDomain()
			}
		}
		if reason != replicaforward.ReasonSealStateUnconfirmed || domain != replicaforward.ErrorDomain {
			t.Errorf("%s: ErrorInfo = %s/%s, want %s/%s", method, domain, reason, replicaforward.ErrorDomain, replicaforward.ReasonSealStateUnconfirmed)
		}
	}

	// It still serves the unseal call.
	allowed := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"}
	if _, err := SealedWindowUnaryInterceptor(stateProbe{sealed: true, unconfirmed: true}, nil)(context.Background(), nil, allowed, dummyHandler); err != nil {
		t.Errorf("unseal on an unconfirmed node: %v, want nil", err)
	}
}

func TestSealedWindow_Unary_SealedAllowlistPasses(t *testing.T) {
	// Enrollment open: the first-boot verbs are on the surface only until
	// the ceremony closes it, and only the listener's gate has an
	// enrollment probe to tell (see
	// TestSealedWindowListener_OfficerRegistrationFollowsEnrollment).
	probe := newProbe(true)
	probe.enrollment.Store(true)
	listener := SealedWindowListenerUnaryInterceptor(probe, nil)
	regular := SealedWindowUnaryInterceptor(probe, nil)
	for _, full := range SealedWindowAllowlist {
		t.Run(full, func(t *testing.T) {
			info := &grpc.UnaryServerInfo{FullMethod: full}
			if _, authOnly := sealedWindowAuthenticatedOnly[full]; !authOnly {
				res, err := listener(context.Background(), nil, info, dummyHandler)
				if err != nil || res != "ok" {
					t.Errorf("listener: res = %v, err = %v, want ok", res, err)
				}
			}
			res, err := regular(context.Background(), nil, info, dummyHandler)
			_, firstBoot := sealedWindowPreOperationalOnly[full]
			if _, listenerOnly := sealedWindowListenerOnly[full]; firstBoot || listenerOnly {
				assertSealedRefusal(t, "regular "+full, err)
				return
			}
			if err != nil || res != "ok" {
				t.Errorf("regular: res = %v, err = %v, want ok", res, err)
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
			assertSealedRefusal(t, method, err)
		})
	}

	// The real QRDBService/BarrierUnseal still passes while sealed.
	if _, err := intercept(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"},
		dummyHandler); err != nil {
		t.Errorf("QRDBService/BarrierUnseal err = %v, want nil", err)
	}
}

// TestSealedWindow_Unary_StatusAndHealthRefused: a sealed node serves the
// unseal call and nothing else. Health, node status and barrier status are
// refused with the typed sealed refusal like every other call; unseal
// progress travels in the unseal call's own response.
func TestSealedWindow_Unary_StatusAndHealthRefused(t *testing.T) {
	gates := map[string]grpc.UnaryServerInterceptor{
		"regular":  SealedWindowUnaryInterceptor(newProbe(true), nil),
		"listener": SealedWindowListenerUnaryInterceptor(newProbe(true), nil),
	}
	for gate, intercept := range gates {
		for _, method := range []string{
			"/qrdb.v1.QRDBService/Health",
			"/qrdb.v1.QRDBService/Status",
			"/qrdb.v1.QRDBService/Get",
		} {
			_, err := intercept(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, dummyHandler)
			assertSealedRefusal(t, gate+" "+method, err)
		}
		if _, err := intercept(context.Background(), nil,
			&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"}, dummyHandler); err != nil {
			t.Errorf("%s BarrierUnseal: %v, want served", gate, err)
		}
	}
}

// TestSealedWindow_Unary_BarrierStatusOnlyOnTheListener: barrier status is
// the initialized flag a sealed node serves, so the sealed window's listener
// serves it, initialized or not; a regular listener refuses it while sealed,
// which is how a held or cut-off node, its own barrier unsealed, answers.
func TestSealedWindow_Unary_BarrierStatusOnlyOnTheListener(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierStatus"}
	for _, enrollment := range []bool{false, true} {
		probe := newProbe(true)
		probe.enrollment.Store(enrollment)
		res, err := SealedWindowListenerUnaryInterceptor(probe, nil)(context.Background(), nil, info, dummyHandler)
		if err != nil || res != "ok" {
			t.Errorf("listener, enrollment open %v: res = %v, err = %v, want ok", enrollment, res, err)
		}
		_, err = SealedWindowUnaryInterceptor(probe, nil)(context.Background(), nil, info, dummyHandler)
		assertSealedRefusal(t, "regular BarrierStatus", err)
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
		&grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"},
		dummyStreamHandler); err != nil {
		t.Errorf("allowlist stream err = %v, want nil", err)
	}
	// Ceremony status is a status answer, which a sealed node does not give.
	err := intercept(nil, &fakeServerStream{},
		&grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/CeremonyStatus"},
		dummyStreamHandler)
	assertSealedRefusal(t, "CeremonyStatus stream", err)
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

// TestSealedWindowListener_RefusesEverythingOnceUnsealed pins the boot-time
// listener's interceptors: that listener has no authentication behind it,
// so once the barrier is unsealed every method -- allowlisted ones
// included -- is refused on the call itself, unary and stream alike.
func TestSealedWindowListener_RefusesEverythingOnceUnsealed(t *testing.T) {
	probe := newProbe(true)
	unary := SealedWindowListenerUnaryInterceptor(probe, nil)
	stream := SealedWindowListenerStreamInterceptor(probe, nil)
	unseal := "/qrdb.v1.QRDBService/BarrierUnseal"

	if _, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: unseal}, dummyHandler); err != nil {
		t.Fatalf("allowlisted method while sealed: %v", err)
	}
	if err := stream(nil, &ctxServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/Watch"}, dummyStreamHandler); status.Code(err) != codes.Unavailable {
		t.Fatalf("non-allowlisted stream while sealed: %v, want Unavailable", err)
	}

	probe.unseal()
	for _, method := range []string{unseal, "/qrdb.v1.QRDBService/BarrierStatus", "/qrdb.v1.QRDBService/KVGet"} {
		_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, dummyHandler)
		st, _ := status.FromError(err)
		if st.Code() != codes.Unavailable || st.Message() != sealedwindow.ClosedMessage {
			t.Errorf("%s after unseal: %v, want Unavailable %q", method, err, sealedwindow.ClosedMessage)
		}
		err = stream(nil, &ctxServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: method}, dummyStreamHandler)
		if status.Code(err) != codes.Unavailable {
			t.Errorf("stream %s after unseal: %v, want Unavailable", method, err)
		}
	}
}

// TestSealedWindowListener_NilProbeFailsClosed: without a probe the
// listener's interceptor cannot know the barrier is sealed, so it refuses.
func TestSealedWindowListener_NilProbeFailsClosed(t *testing.T) {
	_, err := SealedWindowListenerUnaryInterceptor(nil, nil)(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierStatus"}, dummyHandler)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("err = %v, want Unavailable", err)
	}
}

// TestWithSealedWindowListener_SelectsListenerGate verifies the server
// option selects the listener's interceptors and that WithSealedWindowProbe
// applied after it restores the regular ones.
func TestWithSealedWindowListener_SelectsListenerGate(t *testing.T) {
	s := &Server{}
	probe := newProbe(false)
	WithSealedWindowListener(probe)(s)
	if s.sealedWindowProbe == nil || s.sealedWindowListener == nil {
		t.Fatal("WithSealedWindowListener did not install the listener gate")
	}
	WithSealedWindowProbe(probe)(s)
	if s.sealedWindowListener != nil {
		t.Fatal("WithSealedWindowProbe applied last must install the regular gate")
	}
}

// TestSealedWindowListener_OfficerRegistrationFollowsEnrollment: both gates
// refuse officer registration with the sealed refusal once enrollment has
// closed, and admit it while open. A sealed node serves only the unseal call;
// enrollment is the pre-operational exception and nothing past it.
func TestSealedWindowListener_OfficerRegistrationFollowsEnrollment(t *testing.T) {
	probe := newProbe(true)
	gates := map[string]grpc.UnaryServerInterceptor{
		"listener": SealedWindowListenerUnaryInterceptor(probe, nil),
		"regular":  SealedWindowUnaryInterceptor(probe, nil),
	}
	for gate, unary := range gates {
		for _, method := range []string{"/qrdb.v1.QRDBService/BarrierOfficerRegister", "/qrdb.v1.QRDBService/BarrierRecoveryOfficerRegister"} {
			_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, dummyHandler)
			assertSealedRefusal(t, gate+" "+method+", enrollment closed", err)
		}
	}
	unary := gates["listener"]
	if _, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierShareClaimBegin"}, dummyHandler); err != nil {
		t.Errorf("a claim with enrollment closed: %v", err)
	}
	probe.enrollment.Store(true)
	if _, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierOfficerRegister"}, dummyHandler); err != nil {
		t.Errorf("enrollment open: %v", err)
	}
}

// TestSealedWindowListener_MarksCallsAnonymous: unary calls and streams the
// listener admits carry the sealedwindow.Anonymous mark; a regular gate's do
// not.
func TestSealedWindowListener_MarksCallsAnonymous(t *testing.T) {
	probe := newProbe(true)
	info := &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"}
	var anonymous bool
	handler := func(ctx context.Context, _ any) (any, error) {
		anonymous = sealedwindow.Anonymous(ctx)
		return nil, nil
	}
	if _, err := SealedWindowListenerUnaryInterceptor(probe, nil)(context.Background(), nil, info, handler); err != nil || !anonymous {
		t.Errorf("listener unary: anonymous=%v err=%v", anonymous, err)
	}
	if _, err := SealedWindowUnaryInterceptor(probe, nil)(context.Background(), nil, info, handler); err != nil || anonymous {
		t.Errorf("regular unary: anonymous=%v err=%v", anonymous, err)
	}
	stream := &ctxServerStream{ctx: context.Background()}
	streamHandler := func(_ any, ss grpc.ServerStream) error {
		anonymous = sealedwindow.Anonymous(ss.Context())
		return nil
	}
	sinfo := &grpc.StreamServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"}
	if err := SealedWindowListenerStreamInterceptor(probe, nil)(nil, stream, sinfo, streamHandler); err != nil || !anonymous {
		t.Errorf("listener stream: anonymous=%v err=%v", anonymous, err)
	}
}

// ctxServerStream is a grpc.ServerStream with a context.
type ctxServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *ctxServerStream) Context() context.Context { return s.ctx }

// TestSealedWindowListener_NoCallInFlightAcrossUnseal is the gRPC twin of the
// REST in-flight test: an unsealing call waits for the anonymous calls
// already in flight, and calls after it are refused.
func TestSealedWindowListener_NoCallInFlightAcrossUnseal(t *testing.T) {
	probe := newProbe(true)
	unary := SealedWindowListenerUnaryInterceptor(probe, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var sealedAtEnd atomic.Bool
	slow := func(context.Context, any) (any, error) {
		close(entered)
		<-release
		sealedAtEnd.Store(probe.IsSealed())
		return nil, nil
	}
	unseal := func(context.Context, any) (any, error) {
		probe.unseal()
		return nil, nil
	}
	slowDone := make(chan error, 1)
	go func() {
		_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierShareClaimBegin"}, slow)
		slowDone <- err
	}()
	<-entered
	unsealDone := make(chan error, 1)
	go func() {
		_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierUnseal"}, unseal)
		unsealDone <- err
	}()
	select {
	case <-unsealDone:
		close(release)
		<-slowDone
		t.Fatal("the barrier unsealed while an anonymous call admitted under seal was still in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-slowDone; err != nil || !sealedAtEnd.Load() {
		t.Fatalf("in-flight call: err %v, sealed at end %v", err, sealedAtEnd.Load())
	}
	if err := <-unsealDone; err != nil {
		t.Fatalf("unseal: %v", err)
	}
	_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/qrdb.v1.QRDBService/BarrierStatus"}, dummyHandler)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("a call after the unseal: %v, want Unavailable", err)
	}
}

// TestSealedWindowGate_ClusterUnsealIsNeverAnonymous: cluster unseal passes
// the regular gate while it is closed, and the boot-time listener refuses it
// on both the unary and the stream interceptor.
func TestSealedWindowGate_ClusterUnsealIsNeverAnonymous(t *testing.T) {
	probe := newProbe(true)
	probe.enrollment.Store(true)
	const method = "/qrdb.v1.QRDBService/BarrierClusterUnseal"
	if _, err := SealedWindowUnaryInterceptor(probe, nil)(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: method}, dummyHandler); err != nil {
		t.Errorf("regular gate: %v (authentication decides)", err)
	}
	_, err := SealedWindowListenerUnaryInterceptor(probe, nil)(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: method}, dummyHandler)
	if st, _ := status.FromError(err); st.Code() != codes.Unavailable {
		t.Errorf("boot-time listener: %v, want Unavailable", err)
	}
	stream := &ctxServerStream{ctx: context.Background()}
	err = SealedWindowListenerStreamInterceptor(probe, nil)(nil, stream,
		&grpc.StreamServerInfo{FullMethod: method}, func(any, grpc.ServerStream) error { return nil })
	if st, _ := status.FromError(err); st.Code() != codes.Unavailable {
		t.Errorf("boot-time listener stream: %v, want Unavailable", err)
	}
}
