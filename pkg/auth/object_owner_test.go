package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ownerCtx is a resolved account's identity: Subject is the account's
// decimal user id and UserID is bound.
func ownerCtx(userID uint64) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: auth.UserSubject(userID), UserID: userID})
}

// holderCtx is an identity with no bound account (a certificate holder or
// service identity): its Subject is a name, which may be all digits.
func holderCtx(subject string) context.Context {
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
	if err := auth.OwnObject(ownerCtx(1), "1", "read"); err != nil {
		t.Fatalf("OwnObject by the owner = %v, want nil", err)
	}
}

func TestOwnObject_OtherSubjectDenied(t *testing.T) {
	err := auth.OwnObject(ownerCtx(2), "1", "read")
	assertObjectOwnerDenied(t, err, "2", "1")
}

func TestOwnObject_AbsentOwnerDenied(t *testing.T) {
	err := auth.OwnObject(ownerCtx(1), "", "read")
	assertObjectOwnerDenied(t, err, "1", "")
}

func TestOwnObject_NoIdentityDenied(t *testing.T) {
	err := auth.OwnObject(context.Background(), "1", "read")
	assertObjectOwnerDenied(t, err, "", "1")
}

func TestOwnObject_EmptySubjectDenied(t *testing.T) {
	// An empty subject never matches, not even an empty owner.
	assertObjectOwnerDenied(t, auth.OwnObject(holderCtx(""), "1", "read"), "", "1")
	assertObjectOwnerDenied(t, auth.OwnObject(holderCtx(""), "", "read"), "", "")
}

func TestOwnObject_UnboundNumericSubjectDenied(t *testing.T) {
	// A holder named "7" has no bound account (UserID 0); its name matching
	// account 7's owner string must not make it the owner.
	assertObjectOwnerDenied(t, auth.OwnObject(holderCtx("7"), "7", "read"), "7", "7")
	// Account 7 itself still owns the object.
	if err := auth.OwnObject(ownerCtx(7), "7", "read"); err != nil {
		t.Fatalf("OwnObject by bound account 7 = %v, want nil", err)
	}
}

func TestOwnObject_SubjectNotTheBoundAccountDenied(t *testing.T) {
	// The owner is matched against the bound account, never the Subject: an
	// identity bound to account 8 whose Subject reads "7" does not own 7's object.
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "7", UserID: 8})
	assertObjectOwnerDenied(t, auth.OwnObject(ctx, "7", "read"), "7", "7")
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
