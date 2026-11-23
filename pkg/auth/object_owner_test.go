package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func ownerCtx(subject string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject})
}

func assertObjectOwnerDenied(t *testing.T, err error, subject, owner string) {
	t.Helper()
	if err == nil {
		t.Fatal("OwnObject returned nil, want *ObjectOwnerDeniedError")
	}
	var denied *auth.ObjectOwnerDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("error %T %v is not *ObjectOwnerDeniedError", err, err)
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("error %v does not unwrap to ErrUnauthorized", err)
	}
	if denied.Subject != subject || denied.Owner != owner || denied.Op != "read" {
		t.Fatalf("denied = %+v, want subject %q owner %q op read", denied, subject, owner)
	}
	if denied.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestOwnObject_OwnerAllowed(t *testing.T) {
	if err := auth.OwnObject(ownerCtx("user-1"), "user-1", "read"); err != nil {
		t.Fatalf("OwnObject by the owner = %v, want nil", err)
	}
}

func TestOwnObject_OtherSubjectDenied(t *testing.T) {
	err := auth.OwnObject(ownerCtx("user-2"), "user-1", "read")
	assertObjectOwnerDenied(t, err, "user-2", "user-1")
}

func TestOwnObject_AbsentOwnerDenied(t *testing.T) {
	err := auth.OwnObject(ownerCtx("user-1"), "", "read")
	assertObjectOwnerDenied(t, err, "user-1", "")
}

func TestOwnObject_NoIdentityDenied(t *testing.T) {
	err := auth.OwnObject(context.Background(), "user-1", "read")
	assertObjectOwnerDenied(t, err, "", "user-1")
}

func TestOwnObject_EmptySubjectDenied(t *testing.T) {
	// An empty subject never matches, not even an empty owner.
	assertObjectOwnerDenied(t, auth.OwnObject(ownerCtx(""), "user-1", "read"), "", "user-1")
	assertObjectOwnerDenied(t, auth.OwnObject(ownerCtx(""), "", "read"), "", "")
}

func TestResourceOwnerFromContext_UnsetAndEmpty(t *testing.T) {
	if owner, ok := auth.ResourceOwnerFromContext(context.Background()); ok || owner != "" {
		t.Fatalf("unset = (%q, %v), want (\"\", false)", owner, ok)
	}
	ctx := auth.WithResourceOwner(context.Background(), "")
	if owner, ok := auth.ResourceOwnerFromContext(ctx); ok || owner != "" {
		t.Fatalf("empty = (%q, %v), want (\"\", false)", owner, ok)
	}
	ctx = auth.WithResourceOwner(context.Background(), "user-1")
	if owner, ok := auth.ResourceOwnerFromContext(ctx); !ok || owner != "user-1" {
		t.Fatalf("set = (%q, %v), want (\"user-1\", true)", owner, ok)
	}
}
