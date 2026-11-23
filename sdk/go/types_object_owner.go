package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
)

// OwnObject is the object-owner rule, the object twin of OwnTenant (the
// tenant check): it refuses op with *ObjectOwnerDeniedError unless the
// identity on ctx has a non-empty subject equal to the object's non-empty
// recorded owner. It fails closed: no identity, an empty subject or an
// object with no recorded owner is refused. An embedder that keeps its own
// owner check calls this instead. (Aliases pkg/auth.OwnObject.)
var OwnObject = auth.OwnObject

// WithResourceOwner returns a copy of ctx carrying the owner of the object
// the coming operation acts on. The embedder's store sets it server-side,
// after loading the object and before asking the authorizer; the ABAC
// decorator exposes it as resource.owner and evaluates
// subject.owns_resource from it. (Aliases pkg/auth.WithResourceOwner.)
var WithResourceOwner = auth.WithResourceOwner

// ResourceOwnerFromContext returns the owner WithResourceOwner put on ctx;
// ok is false when none, or an empty one, was set.
// (Aliases pkg/auth.ResourceOwnerFromContext.)
var ResourceOwnerFromContext = auth.ResourceOwnerFromContext

// ObjectOwnerDeniedError is OwnObject's refusal; it unwraps to
// auth.ErrUnauthorized, a permission denial.
// (Aliases pkg/auth.ObjectOwnerDeniedError.)
type ObjectOwnerDeniedError = auth.ObjectOwnerDeniedError

// ABACResourceOwnerContextKey is the ABAC check-request context key that
// carries the object owner (resource.owner in a condition).
// (Aliases pkg/auth/abac.ResourceOwnerContextKey.)
const ABACResourceOwnerContextKey = abac.ResourceOwnerContextKey
