package auth

import (
	"context"
	"fmt"
)

// resourceOwnerKey is the context key under which WithResourceOwner stores
// the owner of the object an operation acts on.
type resourceOwnerKey struct{}

// WithResourceOwner returns a copy of ctx carrying ownerID as the owner of
// the object the coming operation acts on. The embedder's store sets it,
// server-side, after it has loaded the object and before it asks the
// authorizer, the same way the target tenant is carried: the owner never
// comes from a request field the caller writes. The ABAC decorator exposes
// it to policy conditions as resource.owner and evaluates
// subject.owns_resource from it.
func WithResourceOwner(ctx context.Context, ownerID string) context.Context {
	return context.WithValue(ctx, resourceOwnerKey{}, ownerID)
}

// ResourceOwnerFromContext returns the object owner WithResourceOwner put
// on ctx. ok is false when no owner was set or the owner set is empty: an
// empty owner is "no recorded owner", never a match.
func ResourceOwnerFromContext(ctx context.Context) (string, bool) {
	owner, _ := ctx.Value(resourceOwnerKey{}).(string)
	if owner == "" {
		return "", false
	}
	return owner, true
}

// ObjectOwnerDeniedError is the refusal OwnObject returns when the calling
// identity is not the recorded owner of the object. It unwraps to
// ErrUnauthorized, so the error class table maps it to PermissionDenied
// (HTTP 403) like an ABAC policy denial.
type ObjectOwnerDeniedError struct {
	// Subject is the calling identity's subject, empty when the context
	// carried no identity or one without a subject.
	Subject string
	// Owner is the object's recorded owner, empty when it has none.
	Owner string
	// Op names the refused operation.
	Op string
}

// Error renders the refusal.
func (e *ObjectOwnerDeniedError) Error() string {
	if e.Owner == "" {
		return fmt.Sprintf("auth: caller %q is not permitted to %s an object with no recorded owner", e.Subject, e.Op)
	}
	return fmt.Sprintf("auth: caller %q is not permitted to %s an object owned by %q", e.Subject, e.Op, e.Owner)
}

// Unwrap returns ErrUnauthorized so errors.Is and the error class table
// treat the refusal as a permission denial.
func (e *ObjectOwnerDeniedError) Unwrap() error { return ErrUnauthorized }

// OwnObject reports whether the identity on ctx owns an object whose
// recorded owner is owner, and refuses op with *ObjectOwnerDeniedError when
// it does not. It is the object twin of tenant.OwnTenant (the tenant
// check) and fails closed: it refuses when ctx carries no identity, when
// the identity has no bound account (UserID 0: a certificate holder or a
// service identity, whose Subject is a name that may be all digits), when
// owner is empty (an object with no recorded owner belongs to nobody), and
// when owner differs from UserSubject(identity.UserID). Ownership is
// decided on the bound account, never on Subject, so a holder named "7"
// does not own account 7's objects; owner must be recorded as
// UserSubject(userID).
func OwnObject(ctx context.Context, owner, op string) error {
	identity := IdentityFromContext(ctx)
	subject := ""
	var userID uint64
	if identity != nil {
		subject = identity.Subject
		userID = identity.UserID
	}
	if userID == 0 || owner == "" || owner != UserSubject(userID) {
		return &ObjectOwnerDeniedError{Subject: subject, Owner: owner, Op: op}
	}
	return nil
}
