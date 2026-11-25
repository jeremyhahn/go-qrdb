package abac

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func TestDecorator_OwnsResourceAllowsTheOwner(t *testing.T) {
	inner := &fakeAuthorizer{err: fakeDeniedError{}}
	policy := testPolicy("owner-grant", "tenant-a", Rule{
		Name:      "owner-reads",
		Effect:    EffectAllow,
		Actions:   []string{"read"},
		Resources: []string{"/objects/**"},
		When:      []Condition{{Expression: "subject.owns_resource"}},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	ctx = auth.WithResourceOwner(ctx, "1")
	if err := authz.Authorize(ctx, accountIdentity(1, "tenant-a"), "/objects/o1", "read"); err != nil {
		t.Fatalf("owner should be allowed, got %v", err)
	}
	if inner.calls != 0 {
		t.Errorf("inner consulted %d times on the owner allow, want 0", inner.calls)
	}
}

func TestDecorator_OwnsResourceFailsClosedWithoutOwner(t *testing.T) {
	inner := &fakeAuthorizer{err: fakeDeniedError{}}
	policy := testPolicy("owner-grant", "tenant-a", Rule{
		Name:      "owner-reads",
		Effect:    EffectAllow,
		Actions:   []string{"read"},
		Resources: []string{"/objects/**"},
		When:      []Condition{{Expression: "subject.owns_resource"}},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	err = authz.Authorize(ctx, accountIdentity(1, "tenant-a"), "/objects/o1", "read")
	if !errors.As(err, new(fakeDeniedError)) {
		t.Fatalf("no owner on ctx should fall to the denying inner, got %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner consulted %d times, want 1", inner.calls)
	}
}

func TestDecorator_OwnsResourceOtherOwnerDenied(t *testing.T) {
	inner := &fakeAuthorizer{}
	policy := testPolicy("owner-only", "tenant-a", Rule{
		Name:      "deny-non-owner",
		Effect:    EffectDeny,
		Actions:   []string{"delete"},
		Resources: []string{"/objects/**"},
		Unless:    []Condition{{Expression: "subject.owns_resource"}},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	ctx = auth.WithResourceOwner(ctx, "2")
	err = authz.Authorize(ctx, accountIdentity(1, "tenant-a"), "/objects/o1", "delete")
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("non-owner should be denied by policy, got %v", err)
	}
	if denied.Policy != "owner-only" {
		t.Errorf("denied by %q, want owner-only", denied.Policy)
	}

	// The owner passes the deny and falls through to the allowing inner.
	ctx = auth.WithResourceOwner(tenant.WithTenantID(context.Background(), "tenant-a"), "1")
	if err := authz.Authorize(ctx, accountIdentity(1, "tenant-a"), "/objects/o1", "delete"); err != nil {
		t.Fatalf("owner should pass the deny rule, got %v", err)
	}
}

// accountIdentity is a resolved account's identity: Subject is the decimal
// user id and UserID is bound.
func accountIdentity(userID uint64, tenantID string) *auth.Identity {
	id := testIdentity(auth.UserSubject(userID), tenantID)
	id.UserID = userID
	return id
}

func TestDecorator_OwnsResourceRefusesUnboundHolder(t *testing.T) {
	inner := &fakeAuthorizer{err: fakeDeniedError{}}
	policy := testPolicy("owner-grant", "tenant-a", Rule{
		Name:      "owner-reads",
		Effect:    EffectAllow,
		Actions:   []string{"read"},
		Resources: []string{"/objects/**"},
		When:      []Condition{{Expression: "subject.owns_resource"}},
	})
	authz, err := Decorate(inner, WithPolicies([]*Policy{policy}))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}

	// A certificate holder named "7" keeps its name as Subject and has no
	// bound account: it must not own account 7's object.
	ctx := auth.WithResourceOwner(tenant.WithTenantID(context.Background(), "tenant-a"), "7")
	err = authz.Authorize(ctx, testIdentity("7", "tenant-a"), "/objects/o1", "read")
	if !errors.As(err, new(fakeDeniedError)) {
		t.Fatalf("unbound holder \"7\" should fall to the denying inner, got %v", err)
	}
	if err := authz.Authorize(ctx, accountIdentity(7, "tenant-a"), "/objects/o1", "read"); err != nil {
		t.Fatalf("account 7 should own its object, got %v", err)
	}
}
