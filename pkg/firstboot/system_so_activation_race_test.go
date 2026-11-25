package firstboot

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// An administrator disabling the pending SO between enrollment's snapshot
// and its activation write must not be reinstated by the activation.
func TestActivateSystemSO_DisableBeforeActivationIsNotReinstated(t *testing.T) {
	so := &user.User{ID: 9, TenantID: "system", Username: "so", Lifecycle: user.LifecyclePending}
	users, deps := committingFixture(so, func(u *user.User) {
		u.Lifecycle = user.LifecycleDisabled
	})
	snapshot := *so
	if err := activateSystemSO(context.Background(), deps, &snapshot); err == nil {
		t.Fatal("activateSystemSO reinstated an account disabled during enrollment")
	}
	if got := users.byName["so"].Lifecycle; got != user.LifecycleDisabled {
		t.Fatalf("lifecycle = %q, want %q", got, user.LifecycleDisabled)
	}
}

// A peer that completed the same enrollment and activated the SO first
// leaves nothing to do: the activation is not a failure.
func TestActivateSystemSO_PeerActivationIsTolerated(t *testing.T) {
	so := &user.User{ID: 9, TenantID: "system", Username: "so", Lifecycle: user.LifecyclePending}
	users, deps := committingFixture(so, func(u *user.User) {
		u.Lifecycle = user.LifecycleActive
	})
	snapshot := *so
	if err := activateSystemSO(context.Background(), deps, &snapshot); err != nil {
		t.Fatalf("activateSystemSO after a peer's activation: %v", err)
	}
	if got := users.byName["so"].Lifecycle; got != user.LifecycleActive {
		t.Fatalf("lifecycle = %q, want %q", got, user.LifecycleActive)
	}
}
