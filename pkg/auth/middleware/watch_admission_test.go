package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// pushWatcher is a watcher a test pushes responses onto.
type pushWatcher struct {
	events chan watch.WatchResponse
}

func (w *pushWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *pushWatcher) WatchID() int64                     { return 1 }
func (w *pushWatcher) Close() error                       { return nil }
func (w *pushWatcher) Err() error                         { return nil }

// pushWatchService opens every prefix watch on the same pushWatcher.
type pushWatchService struct {
	service.WatchServicer
	w *pushWatcher
}

func (s *pushWatchService) WatchPrefix(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return s.w, nil
}

// openAdmittedWatch opens a prefix watch for identity through the
// middleware's per-event admission and returns the source to push onto.
func openAdmittedWatch(t *testing.T, m *AuthMiddleware, identity *auth.Identity) (*pushWatcher, watch.Watcher) {
	t.Helper()
	src := &pushWatcher{events: make(chan watch.WatchResponse, 2)}
	w, err := service.NewAdmittedWatch(&pushWatchService{w: src}, m).
		WatchPrefix(auth.WithIdentity(context.Background(), identity), "")
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return src, w
}

// A watch opened while acme was active ends on its next event after acme
// is suspended, with the same *TenantNotActiveError a new request gets.
func TestWatchAdmission_SuspensionEndsOpenWatch(t *testing.T) {
	tenants := statusTenants(t)
	m := newTestMiddleware(&mockAuthorizer{}, WithTenantStatusReader(tenants))
	src, w := openAdmittedWatch(t, m, &auth.Identity{Subject: "bob", TenantID: "acme"})

	src.events <- watch.WatchResponse{Revision: 1}
	if _, ok := <-w.Events(); !ok {
		t.Fatal("event while active not delivered")
	}

	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	src.events <- watch.WatchResponse{Revision: 2}
	if resp, ok := <-w.Events(); ok {
		t.Fatalf("event after suspension delivered: %+v", resp)
	}
	wantNotActive(t, "watch Err", w.Err(), tenant.StatusSuspended)
}

// A system-tenant operator's watch is not refused because another tenant
// was suspended.
func TestWatchAdmission_SystemWatchSurvivesOtherTenantSuspension(t *testing.T) {
	tenants := statusTenants(t)
	m := newTestMiddleware(&mockAuthorizer{}, WithTenantStatusReader(tenants))
	src, w := openAdmittedWatch(t, m, &auth.Identity{Subject: "root", TenantID: tenantid.System})

	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	src.events <- watch.WatchResponse{Revision: 3}
	if resp, ok := <-w.Events(); !ok || resp.Revision != 3 {
		t.Fatalf("system watch event = %+v, %v; want revision 3 delivered", resp, ok)
	}
	if err := w.Err(); err != nil {
		t.Fatalf("system watch Err() = %v, want nil", err)
	}
}

// A tenant status that cannot be read ends the watch: the check fails
// closed with *StoreUnavailableError rather than delivering.
func TestWatchAdmission_StatusReadFailureEndsWatch(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{}, WithTenantStatusReader(&countingTenants{err: errors.New("io")}))
	src, w := openAdmittedWatch(t, m, &auth.Identity{Subject: "bob", TenantID: "acme"})

	src.events <- watch.WatchResponse{Revision: 1}
	if resp, ok := <-w.Events(); ok {
		t.Fatalf("event delivered with unreadable status: %+v", resp)
	}
	var unavailable *StoreUnavailableError
	if !errors.As(w.Err(), &unavailable) {
		t.Fatalf("watch Err() = %T(%v), want *StoreUnavailableError", w.Err(), w.Err())
	}
}

// The middleware is the admitter the server installs.
var _ service.StreamAdmitter = (*AuthMiddleware)(nil)
