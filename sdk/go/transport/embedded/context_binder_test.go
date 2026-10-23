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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// errBinderRefused is the refusing binder's sentinel.
var errBinderRefused = errors.New("binder refused")

// binderKey is the context key the accepting binder sets.
type binderKey struct{}

// refusingBinder refuses every call and counts how often it ran.
func refusingBinder(calls *atomic.Int32) ContextBinder {
	return func(ctx context.Context) (context.Context, error) {
		calls.Add(1)
		return ctx, errBinderRefused
	}
}

// identityBinder binds the identity carol and a marker value, counting runs.
func identityBinder(calls *atomic.Int32) ContextBinder {
	return func(ctx context.Context) (context.Context, error) {
		calls.Add(1)
		ctx = context.WithValue(ctx, binderKey{}, "bound")
		return auth.WithIdentity(ctx, &auth.Identity{Subject: "carol", TenantID: tenantid.System}), nil
	}
}

// markerOf returns the accepting binder's marker on ctx, or "" when the
// context was not bound.
func markerOf(ctx context.Context) string {
	v, _ := ctx.Value(binderKey{}).(string)
	return v
}

// countingKV counts every Put and Get that reaches the KV service and records
// the marker value on the last context it saw.
type countingKV struct {
	*mockKVService
	calls atomic.Int32
	seen  atomic.Value
}

func (k *countingKV) Put(ctx context.Context, key string, value []byte) error {
	k.calls.Add(1)
	k.seen.Store(markerOf(ctx))
	return k.mockKVService.Put(ctx, key, value)
}

func (k *countingKV) Get(ctx context.Context, key string) ([]byte, error) {
	k.calls.Add(1)
	k.seen.Store(markerOf(ctx))
	return k.mockKVService.Get(ctx, key)
}

// recordingAuthorizer grants every request and records the marker value and
// the identity on the context the middleware handed it.
type recordingAuthorizer struct {
	calls   atomic.Int32
	marker  atomic.Value
	subject atomic.Value
}

func (a *recordingAuthorizer) Authorize(ctx context.Context, identity *auth.Identity, _, _ string) error {
	a.calls.Add(1)
	a.marker.Store(markerOf(ctx))
	if identity != nil {
		a.subject.Store(identity.Subject)
	}
	return nil
}

// binderDoubles is the set of counting service doubles a refusal must leave
// untouched.
type binderDoubles struct {
	kv         *countingKV
	barrier    atomic.Int32
	ceremony   atomic.Int32
	credential atomic.Int32
	watch      atomic.Int32
}

// handlerCalls totals every handler the doubles saw.
func (d *binderDoubles) handlerCalls() int32 {
	return d.kv.calls.Load() + d.barrier.Load() + d.ceremony.Load() + d.credential.Load() + d.watch.Load()
}

// newBinderTransport builds a transport over counting doubles with the given
// extra options.
func newBinderTransport(t *testing.T, opts ...Option) (*Transport, *binderDoubles) {
	t.Helper()
	d := &binderDoubles{kv: &countingKV{mockKVService: newMockKVService()}}
	base := []Option{
		WithAuditLogger(audit.NewNoopLogger()),
		WithBarrierService(&mockBarrierService{
			barrierStatusFn: func(context.Context) (*service.BarrierStatusInfo, error) {
				d.barrier.Add(1)
				return &service.BarrierStatusInfo{}, nil
			},
			barrierSealFn: func(context.Context) error {
				d.barrier.Add(1)
				return nil
			},
		}),
		WithCeremonyService(&mockCeremonyService{
			statusFn: func(context.Context) (*service.CeremonyStatusInfo, error) {
				d.ceremony.Add(1)
				return &service.CeremonyStatusInfo{}, nil
			},
		}),
		WithCredentialService(&mockCredentialService{
			listFn: func(context.Context) ([]string, error) {
				d.credential.Add(1)
				return nil, nil
			},
		}),
		WithWatchService(&mockWatchService{
			watchFn: func(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
				d.watch.Add(1)
				w := newStubWatcher()
				close(w.ch)
				return w, nil
			},
		}),
	}
	tr, err := New(d.kv, &mockAdminService{}, append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr, d
}

// binderCalls is one call per service family the binder must stop.
var binderCalls = []struct {
	name string
	call func(context.Context, *Transport) error
}{
	{"BarrierStatus", func(ctx context.Context, tr *Transport) error { _, err := tr.BarrierStatus(ctx); return err }},
	{"BarrierSeal", func(ctx context.Context, tr *Transport) error { return tr.BarrierSeal(ctx) }},
	{"CeremonyStatus", func(ctx context.Context, tr *Transport) error { _, err := tr.CeremonyStatus(ctx); return err }},
	{"CredentialList", func(ctx context.Context, tr *Transport) error {
		_, err := tr.CredentialList(ctx, &transport.CredentialListRequest{})
		return err
	}},
	{"Put", func(ctx context.Context, tr *Transport) error { return tr.Put(ctx, "k", []byte("v")) }},
	{"Get", func(ctx context.Context, tr *Transport) error { _, err := tr.Get(ctx, "k"); return err }},
	{"Request(Put)", func(ctx context.Context, tr *Transport) error {
		return tr.Request(ctx, "Put", &putRequest{Key: "k", Value: []byte("v")}, nil)
	}},
	{"Watch", func(ctx context.Context, tr *Transport) error {
		_, err := tr.Watch(ctx, "k", transport.WatchOptions{})
		return err
	}},
}

// TestContextBinder_RefusalStopsEveryServiceFamily: a refusing binder fails
// barrier, ceremony, credential, KV, generic and streaming calls with its own
// error, and no handler is reached.
func TestContextBinder_RefusalStopsEveryServiceFamily(t *testing.T) {
	var runs atomic.Int32
	tr, d := newBinderTransport(t, WithContextBinder(refusingBinder(&runs)))
	for _, c := range binderCalls {
		if err := c.call(context.Background(), tr); !errors.Is(err, errBinderRefused) {
			t.Errorf("%s: err = %v, want the binder's error", c.name, err)
		}
	}
	if got := d.handlerCalls(); got != 0 {
		t.Fatalf("handlers reached = %d, want 0", got)
	}
	if got, want := runs.Load(), int32(len(binderCalls)); got != want {
		t.Fatalf("binder ran %d times, want %d (once per call)", got, want)
	}
}

// TestContextBinder_NoBinderDispatches: with no binder the same calls reach
// their handlers with the caller's context untouched.
func TestContextBinder_NoBinderDispatches(t *testing.T) {
	tr, d := newBinderTransport(t)
	for _, c := range binderCalls {
		if err := c.call(context.Background(), tr); err != nil {
			t.Errorf("%s: err = %v, want nil", c.name, err)
		}
	}
	if got := d.handlerCalls(); got != int32(len(binderCalls)) {
		t.Fatalf("handlers reached = %d, want %d", got, len(binderCalls))
	}
	if v := d.kv.seen.Load(); v != "" {
		t.Fatalf("KV handler saw marker %v with no binder installed", v)
	}
}

// TestContextBinder_BoundContextReachesMiddlewareAndHandler: the accepting
// binder's identity and value are what the auth middleware and the handler
// see, though the caller passed a bare context.
func TestContextBinder_BoundContextReachesMiddlewareAndHandler(t *testing.T) {
	var runs atomic.Int32
	authz := &recordingAuthorizer{}
	mw := authmw.New(authz, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	tr, d := newBinderTransport(t, WithContextBinder(identityBinder(&runs)), WithAuthMiddleware(mw))
	if err := tr.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if authz.calls.Load() == 0 {
		t.Fatal("the auth middleware never reached its authorizer")
	}
	if got := authz.marker.Load(); got != "bound" {
		t.Errorf("middleware saw marker %v, want bound", got)
	}
	if got := authz.subject.Load(); got != "carol" {
		t.Errorf("middleware saw subject %v, want carol", got)
	}
	if got := d.kv.seen.Load(); got != "bound" {
		t.Errorf("handler saw marker %v, want bound", got)
	}
}

// TestContextBinder_UnboundCallerIsUnauthenticated: without the binder the
// same bare context is refused by the middleware, so the binder is what
// supplied the identity in the test above.
func TestContextBinder_UnboundCallerIsUnauthenticated(t *testing.T) {
	mw := authmw.New(&recordingAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	tr, d := newBinderTransport(t, WithAuthMiddleware(mw))
	if err := tr.Put(context.Background(), "k", []byte("v")); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Put without identity: err = %v, want auth.ErrUnauthenticated", err)
	}
	if got := d.kv.calls.Load(); got != 0 {
		t.Fatalf("KV handler reached %d times, want 0", got)
	}
}

// TestContextBinder_BindsOncePerCall: Request re-enters Put, and a
// database-scoped clone shares the transport's binder; both bind once.
func TestContextBinder_BindsOncePerCall(t *testing.T) {
	var runs atomic.Int32
	tr, d := newBinderTransport(t, WithContextBinder(identityBinder(&runs)))
	if err := tr.Request(context.Background(), "Put", &putRequest{Key: "k", Value: []byte("v")}, nil); err != nil {
		t.Fatalf("Request(Put): %v", err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("Request(Put) ran the binder %d times, want 1", got)
	}
	if got := d.kv.seen.Load(); got != "bound" {
		t.Fatalf("handler saw marker %v, want bound", got)
	}
	scoped := tr.cloneShared()
	if scoped.binder != tr.binder {
		t.Fatal("a database-scoped clone does not share the transport's binder")
	}
	ctx, err := tr.bind(context.Background(), "Put")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.bind(ctx, "Put"); err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("a context bound by the transport was bound again by its clone: runs = %d, want 2", got)
	}
}

// TestContextBinder_NilContextIsRefused: a binder that returns a nil context
// and no error fails the call with *NilBoundContextError naming the method.
func TestContextBinder_NilContextIsRefused(t *testing.T) {
	nilBinder := func(context.Context) (context.Context, error) {
		return nil, nil //nolint:staticcheck // the defect under test
	}
	tr, d := newBinderTransport(t, WithContextBinder(nilBinder))
	var nilCtx *NilBoundContextError
	if err := tr.Put(context.Background(), "k", []byte("v")); !errors.As(err, &nilCtx) {
		t.Fatalf("Put: err = %v, want *NilBoundContextError", err)
	}
	if nilCtx.Method != "Put" {
		t.Errorf("Method = %q, want Put", nilCtx.Method)
	}
	if _, err := tr.BarrierStatus(context.Background()); !errors.As(err, &nilCtx) || nilCtx.Method != "BarrierStatus" {
		t.Errorf("BarrierStatus: err = %v, want *NilBoundContextError for BarrierStatus", err)
	}
	if got := d.handlerCalls(); got != 0 {
		t.Fatalf("handlers reached = %d, want 0", got)
	}
}

// TestContextBinder_NilBinderFailsNew: WithContextBinder(nil) is a typed
// construction error, not a panic at the first call.
func TestContextBinder_NilBinderFailsNew(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{}, WithContextBinder(nil))
	var nilBinder *NilContextBinderError
	if !errors.As(err, &nilBinder) {
		t.Fatalf("New: err = %v, want *NilContextBinderError", err)
	}
	if tr != nil {
		t.Fatal("New returned a transport alongside the error")
	}
}

// TestContextBinder_LastOptionWins: a second WithContextBinder replaces the
// first, so a nil binder installed after a real one still fails New.
func TestContextBinder_LastOptionWins(t *testing.T) {
	var runs atomic.Int32
	if _, err := New(newMockKVService(), &mockAdminService{},
		WithContextBinder(identityBinder(&runs)), WithContextBinder(nil)); err == nil {
		t.Fatal("New accepted a nil binder installed after a real one")
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithContextBinder(refusingBinder(&runs)), WithContextBinder(identityBinder(&runs)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.bind(context.Background(), "Put"); err != nil {
		t.Fatalf("the later, accepting binder did not replace the refusing one: %v", err)
	}
}

// TestContextBinder_ErrorStrings pins both errors' messages.
func TestContextBinder_ErrorStrings(t *testing.T) {
	if got, want := (&NilContextBinderError{}).Error(), "embedded: context binder must not be nil"; got != want {
		t.Errorf("NilContextBinderError = %q, want %q", got, want)
	}
	if got, want := (&NilBoundContextError{Method: "Get"}).Error(),
		"embedded: Get: context binder returned a nil context"; got != want {
		t.Errorf("NilBoundContextError = %q, want %q", got, want)
	}
}

// TestContextBinder_RefusesEveryRequestMethod walks every request method of
// *Transport and of each sub-client by reflection: each one must fail with
// the refusing binder's error. An entry point that dispatches without admit
// fails here. The discovery floors are the seal gate's, since the binder
// covers the gate's entry points and the allowlisted ones besides.
func TestContextBinder_RefusesEveryRequestMethod(t *testing.T) {
	var runs atomic.Int32
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithContextBinder(refusingBinder(&runs)), WithAuditLogger(audit.NewNoopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	v := reflect.ValueOf(tr)
	walked := 0
	for _, m := range requestMethods() {
		walked++
		if err := callZero(v, m); !errors.Is(err, errBinderRefused) {
			t.Errorf("%s with a refusing binder: err = %v, want the binder's error", m.Name, err)
		}
	}
	if walked < minGatedMethods {
		t.Fatalf("walked %d request methods, want at least %d -- the walk found too few", walked, minGatedMethods)
	}

	trPtr := v.Pointer()
	subWalked := 0
	for _, acc := range subClientAccessors() {
		client := v.MethodByName(acc.Name).Call(nil)[0]
		if client.IsNil() {
			t.Errorf("%s() returned nil; the test must wire what it needs", acc.Name)
			continue
		}
		dyn := client.Elem()
		if dyn.Kind() == reflect.Pointer && dyn.Pointer() == trPtr {
			continue
		}
		for i := 0; i < dyn.NumMethod(); i++ {
			m := dyn.Type().Method(i)
			if ft := m.Type; ft.NumOut() == 0 || ft.Out(ft.NumOut()-1) != errorType {
				continue
			}
			subWalked++
			if err := callZeroBound(dyn.Method(i)); !errors.Is(err, errBinderRefused) {
				t.Errorf("%s.%s with a refusing binder: err = %v, want the binder's error", acc.Name, m.Name, err)
			}
		}
	}
	if subWalked < minGatedSubClientMethods {
		t.Fatalf("walked %d sub-client methods, want at least %d -- the walk found too few", subWalked, minGatedSubClientMethods)
	}
}
