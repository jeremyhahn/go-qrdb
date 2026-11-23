package auth

import (
	"context"
	"strconv"
)

// PeerCredential is the operating-system identity of the process at the
// other end of a local (Unix-domain) socket, as the kernel reports it at
// connect time (SO_PEERCRED on Linux). It is never taken from anything the
// peer sends: only the Unix transport, after reading it from the socket
// itself, binds it into a request context.
type PeerCredential struct {
	// UID is the peer process's effective user id.
	UID uint32
	// GID is the peer process's effective primary group id.
	GID uint32
	// PID is the peer process's id at connect time (informational only).
	PID int32
}

type peerCredentialContextKey struct{}

// WithPeerCredential returns a context carrying the peer credential of the
// connection the request arrived on. Only the Unix transport calls it.
func WithPeerCredential(ctx context.Context, cred PeerCredential) context.Context {
	return context.WithValue(ctx, peerCredentialContextKey{}, cred)
}

// PeerCredentialFromContext returns the peer credential WithPeerCredential
// bound into ctx, and whether one was bound.
func PeerCredentialFromContext(ctx context.Context) (PeerCredential, bool) {
	cred, ok := ctx.Value(peerCredentialContextKey{}).(PeerCredential)
	return cred, ok
}

// MethodPeerCred is the name the peer-credential authenticator reports, and
// the token an account's permitted authentication methods (user.User
// AuthMethods) name it by.
const MethodPeerCred = "peercred"

// PeerCredentialMapping binds a local Unix account to a go-qrdb user
// account. Exactly one of UID and GID is set. The mapping names an ACCOUNT,
// never a role (iam-exposure item (2), authz-claim-roles): the identity it
// yields carries the bound user's roles, resolved from the user store by the
// authentication middleware exactly as for a client certificate.
type PeerCredentialMapping struct {
	// UID, when set, matches a peer whose uid equals it.
	UID *uint32
	// GID, when set, matches a peer whose primary gid equals it.
	GID *uint32
	// TenantID is the tenant of the bound account; empty means the system
	// tenant (the middleware normalizes it).
	TenantID string
	// Username is the go-qrdb username of the bound account.
	Username string
}

// PeerCredentialAuthenticator authenticates a caller on a local socket by
// the peer credential the transport read from the socket, through an
// operator-configured mapping from uid or gid to a go-qrdb account. A uid
// mapping wins over a gid mapping; a peer no mapping names is refused with
// ErrPeerCredentialUnmapped (default deny). The identity it returns carries
// no roles: the middleware fills them from the bound account. A mapped
// account that does not exist (never created, or deleted), or that cannot
// be resolved because no user store is configured, is refused with
// ErrPeerCredentialAccountMissing; a disabled account or one not permitting
// MethodPeerCred is refused as for any other method.
type PeerCredentialAuthenticator struct {
	mappings []PeerCredentialMapping
}

// NewPeerCredentialAuthenticator returns an authenticator over a copy of
// mappings.
func NewPeerCredentialAuthenticator(mappings []PeerCredentialMapping) *PeerCredentialAuthenticator {
	return &PeerCredentialAuthenticator{mappings: append([]PeerCredentialMapping(nil), mappings...)}
}

// Name returns MethodPeerCred.
func (a *PeerCredentialAuthenticator) Name() string {
	return MethodPeerCred
}

// Authenticate maps the context's peer credential to a go-qrdb account.
func (a *PeerCredentialAuthenticator) Authenticate(ctx context.Context) (*Identity, error) {
	cred, ok := PeerCredentialFromContext(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	m, ok := a.match(cred)
	if !ok {
		return nil, ErrPeerCredentialUnmapped
	}
	return &Identity{
		Subject:  m.Username,
		TenantID: m.TenantID,
		Metadata: map[string]string{
			"peer_uid": strconv.FormatUint(uint64(cred.UID), 10),
			"peer_gid": strconv.FormatUint(uint64(cred.GID), 10),
			"peer_pid": strconv.FormatInt(int64(cred.PID), 10),
		},
	}, nil
}

// match returns the mapping for cred: a uid mapping first, then a gid one.
func (a *PeerCredentialAuthenticator) match(cred PeerCredential) (PeerCredentialMapping, bool) {
	for _, m := range a.mappings {
		if m.UID != nil && *m.UID == cred.UID {
			return m, true
		}
	}
	for _, m := range a.mappings {
		if m.UID == nil && m.GID != nil && *m.GID == cred.GID {
			return m, true
		}
	}
	return PeerCredentialMapping{}, false
}
