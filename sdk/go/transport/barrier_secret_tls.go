// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// BarrierSecretKind names the kind of barrier secret a request carries in its
// body -- never the secret's value. It is the Kind of a
// BarrierSecretRequiresTLSError.
type BarrierSecretKind string

const (
	// BarrierSecretInit is the secret a barrier is initialized with.
	BarrierSecretInit BarrierSecretKind = "init secret"
	// BarrierSecretUnsealKey is the key a barrier is unsealed with.
	BarrierSecretUnsealKey BarrierSecretKind = "unseal key"
	// BarrierSecretPassphrase is the passphrase a seal strategy wraps a
	// barrier's key with.
	BarrierSecretPassphrase BarrierSecretKind = "passphrase"
	// BarrierSecretShare is a holder's Shamir share, submitted to a quorum.
	BarrierSecretShare BarrierSecretKind = "Shamir share"
	// BarrierSecretUserPIN is the user PIN provisioned into a node's PIN
	// backend.
	BarrierSecretUserPIN BarrierSecretKind = "user PIN"

	// soPINSecret is a request's own SO PIN field. It is refused with
	// SOPINRequiresTLSError, the SO PIN's error however the caller supplied
	// the PIN.
	soPINSecret BarrierSecretKind = "SO PIN"
)

// BarrierSecretCarrier is a request whose body can carry a barrier secret.
// Every such request type in this package implements it, and only those; its
// method is unexported, so the set of secret-bearing requests is the list in
// this file.
type BarrierSecretCarrier interface {
	// barrierSecret returns the kind of the first barrier secret the request
	// carries, in field order, or "" when it carries none. A nil request
	// carries none.
	barrierSecret() BarrierSecretKind
}

// RequireTLSForBarrierSecret refuses a request carrying a barrier secret on a
// connection that is not TLS: it returns *BarrierSecretRequiresTLSError, or
// *SOPINRequiresTLSError when the secret is the request's own SO PIN. A
// request carrying no secret, or one on a TLS connection, is let through. A
// transport calls it before it builds the request, so a refused secret is
// never sent.
func RequireTLSForBarrierSecret(transportName string, tlsConn bool, req BarrierSecretCarrier) error {
	if tlsConn {
		return nil
	}
	if kind := req.barrierSecret(); kind != "" {
		return barrierSecretTLSError(transportName, kind)
	}
	return nil
}

// GuardBarrierSecret is RequireTLSForBarrierSecret for the HTTP transports,
// REST and MCP. When the request carries a secret and the connection is TLS it
// returns ctx marked with the secret's kind, which the round tripper
// SOPINRoundTripper builds reads on every hop: a hop that is not https -- a
// redirect to http:// included -- is refused before it is sent, exactly as it
// is for the SO PIN, and so is a redirect to another host
// (*BarrierSecretForeignHostError).
func GuardBarrierSecret(ctx context.Context, transportName string, tlsConn bool, req BarrierSecretCarrier) (context.Context, error) {
	if err := RequireTLSForBarrierSecret(transportName, tlsConn, req); err != nil {
		return ctx, err
	}
	if kind := req.barrierSecret(); kind != "" {
		return context.WithValue(ctx, barrierSecretKey{}, kind), nil
	}
	return ctx, nil
}

// IsHTTPS reports whether address is an https:// URL, the only scheme on
// which the REST and MCP transports send a barrier secret or the SO PIN.
func IsHTTPS(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https"
}

// IsHTTP reports whether address names the plaintext http:// scheme
// explicitly. The gRPC and QUIC transports treat such an address as not TLS
// for barrier secrets and the SO PIN, whatever their TLS settings say: the
// caller asked for plaintext.
func IsHTTP(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "http"
}

// barrierSecretKey is the context key GuardBarrierSecret marks a request's
// context with.
type barrierSecretKey struct{}

// barrierSecretInContext returns the kind of barrier secret ctx was marked
// with by GuardBarrierSecret.
func barrierSecretInContext(ctx context.Context) (BarrierSecretKind, bool) {
	kind, ok := ctx.Value(barrierSecretKey{}).(BarrierSecretKind)
	return kind, ok
}

// barrierSecretTLSError is the typed refusal for a secret of kind on a
// connection that is not TLS.
func barrierSecretTLSError(transportName string, kind BarrierSecretKind) error {
	if kind == soPINSecret {
		return &SOPINRequiresTLSError{Transport: transportName}
	}
	return &BarrierSecretRequiresTLSError{Transport: transportName, Kind: kind}
}

// secretIf returns kind when set, and "" otherwise; cmp.Or over a request's
// fields picks the first secret it carries.
func secretIf(set bool, kind BarrierSecretKind) BarrierSecretKind {
	if set {
		return kind
	}
	return ""
}

// BarrierSecretRequiresTLSError is returned when a request carrying a barrier
// secret in its body -- an init secret, unseal key, passphrase, Shamir share
// or user PIN -- would travel over a connection that is not TLS: a plaintext
// gRPC or Unix socket connection, or an http:// hop on REST or MCP, a redirect
// to one included. The request is refused before it is sent, so the secret
// never crosses a plaintext connection.
type BarrierSecretRequiresTLSError struct {
	// Transport names the SDK transport that refused ("rest", "grpc", ...).
	Transport string
	// Kind names the secret the request carried, never its value.
	Kind BarrierSecretKind
}

// Error returns a description that never contains the secret.
func (e *BarrierSecretRequiresTLSError) Error() string {
	return "transport: " + e.Transport + ": the " + string(e.Kind) + " is sent only over TLS; this connection is not TLS"
}

// BarrierSecretForeignHostError is returned when a REST or MCP request carrying
// a barrier secret in its body is redirected to a host other than the one it
// was sent to. The redirected request is refused before it is sent, so a 307
// or 308 does not resend the secret to that host. A redirect to the same host
// is refused too, with *BarrierSecretRedirectError.
type BarrierSecretForeignHostError struct {
	// Transport names the SDK transport that refused ("rest", "mcp").
	Transport string
	// Kind names the secret the request carried, never its value.
	Kind BarrierSecretKind
	// OriginalHost is the host[:port] the request was sent to.
	OriginalHost string
	// RedirectHost is the host[:port] the redirect named.
	RedirectHost string
}

// Error returns a description that never contains the secret.
func (e *BarrierSecretForeignHostError) Error() string {
	return "transport: " + e.Transport + ": the " + string(e.Kind) + " is sent only to " + e.OriginalHost +
		"; refusing to follow a redirect to " + e.RedirectHost
}

// barrierSecretHopError returns the typed refusal for one hop of a request
// whose body carries a secret of kind, or nil when the hop may be sent: a hop
// that is not https is refused, and so is every redirect: to a host other than
// the one the request was first sent to with the foreign-host error, to the
// same host with *BarrierSecretRedirectError.
func barrierSecretHopError(transportName string, kind BarrierSecretKind, req *http.Request) error {
	if req.URL.Scheme != "https" {
		return barrierSecretTLSError(transportName, kind)
	}
	first := firstHop(req)
	if first.URL.Host != req.URL.Host {
		if kind == soPINSecret {
			return &SOPINForeignHostError{Transport: transportName, Host: req.URL.Host}
		}
		return &BarrierSecretForeignHostError{
			Transport:    transportName,
			Kind:         kind,
			OriginalHost: first.URL.Host,
			RedirectHost: req.URL.Host,
		}
	}
	if req.Response != nil {
		return barrierSecretRedirectError(transportName, kind, req.Response.StatusCode)
	}
	return nil
}

// barrierSecretRedirectError is the typed refusal for a redirect hop, to the
// same host included, of a request carrying a secret of kind.
func barrierSecretRedirectError(transportName string, kind BarrierSecretKind, status int) error {
	if kind == soPINSecret {
		return &SOPINRedirectError{Transport: transportName, Status: status}
	}
	return &BarrierSecretRedirectError{Transport: transportName, Kind: kind, Status: status}
}

// BarrierSecretRedirectError is returned when a REST or MCP request carrying a
// barrier secret in its body is answered with a redirect, to the same host
// included. The redirected request is refused before it is sent: a secret is
// sent once, to the URL the caller configured, and never replayed to a path
// the server names.
type BarrierSecretRedirectError struct {
	// Transport names the SDK transport that refused ("rest", "mcp").
	Transport string
	// Kind names the secret the request carried, never its value.
	Kind BarrierSecretKind
	// Status is the HTTP status of the redirect response (307, 308, ...).
	Status int
}

// Error returns a description that never contains the secret.
func (e *BarrierSecretRedirectError) Error() string {
	return "transport: " + e.Transport + ": the " + string(e.Kind) + " is never resent on a redirect; refusing to follow a " +
		strconv.Itoa(e.Status) + " " + http.StatusText(e.Status)
}

// firstHop returns the request an http.Client was first given, following the
// chain of redirect responses back from req; req itself when it is not a
// redirect.
func firstHop(req *http.Request) *http.Request {
	for req.Response != nil && req.Response.Request != nil {
		req = req.Response.Request
	}
	return req
}

func (r *BarrierInitializeRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return cmp.Or(
		secretIf(len(r.Secret) > 0, BarrierSecretInit),
		secretIf(r.SOPin != "", soPINSecret),
		secretIf(r.UserPin != "", BarrierSecretUserPIN),
	)
}

func (r *BarrierUnsealRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Secret) > 0, BarrierSecretUnsealKey)
}

func (r *BarrierChangePassphraseRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Current) > 0 || len(r.Next) > 0, BarrierSecretPassphrase)
}

func (r *BarrierInitializeShamirRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return cmp.Or(
		secretIf(r.SOPin != "", soPINSecret),
		secretIf(r.UserPin != "", BarrierSecretUserPIN),
	)
}

func (r *BarrierEnableEncryptionRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Passphrase) > 0, BarrierSecretPassphrase)
}

func (r *BarrierGenerateRecoveryKeysRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(r.SOPin != "", soPINSecret)
}

func (r *QuorumSubmitRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Share) > 0, BarrierSecretShare)
}

func (r *RekeyStartRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(r.SOPin != "", soPINSecret)
}

func (r *OfficerRevokeRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(r.SOPin != "", soPINSecret)
}

func (r *CeremonyInitializeRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return cmp.Or(
		secretIf(r.SOPin != "", soPINSecret),
		secretIf(r.UserPin != "", BarrierSecretUserPIN),
		secretIf(len(r.Secret) > 0, BarrierSecretInit),
	)
}

func (r *SignCSRInitRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(r.SOPin != "", soPINSecret)
}

func (r *TenantBarrierInitRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Passphrase) > 0, BarrierSecretPassphrase)
}

func (r *TenantBarrierSignedInitCompleteRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Passphrase) > 0, BarrierSecretPassphrase)
}

func (r *TenantBarrierUnsealRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Key) > 0, BarrierSecretUnsealKey)
}

func (r *TenantQuorumSubmitRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(len(r.Share) > 0, BarrierSecretShare)
}

// barrierSecret reports the SO PIN when tenant creation enrols an SO with
// one; a request without a PIN carries no secret and is sent as before.
func (r *CreateTenantRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return secretIf(r.SOPin != "", soPINSecret)
}

// barrierSecret is declared rather than promoted from QuorumSubmitRequest: the
// promoted method dereferences a nil request.
func (r *DatabaseQuorumSubmitRequest) barrierSecret() BarrierSecretKind {
	if r == nil {
		return ""
	}
	return r.QuorumSubmitRequest.barrierSecret()
}

func (c *DatabaseBarrierConfig) barrierSecret() BarrierSecretKind {
	if c == nil {
		return ""
	}
	return secretIf(c.Secret != "", BarrierSecretPassphrase)
}

func (r CreateDatabaseRequest) barrierSecret() BarrierSecretKind {
	return r.Barrier.barrierSecret()
}

func (r UpdateDatabaseBarrierRequest) barrierSecret() BarrierSecretKind {
	return r.Barrier.barrierSecret()
}

// DatabaseUnsealSecret is the BarrierSecretCarrier for UnsealDatabase, whose
// secret is an argument rather than a request field.
func DatabaseUnsealSecret(secret string) BarrierSecretCarrier {
	return databaseUnsealSecret(secret != "")
}

// databaseUnsealSecret records only whether an UnsealDatabase secret was
// given, never the secret.
type databaseUnsealSecret bool

func (s databaseUnsealSecret) barrierSecret() BarrierSecretKind {
	return secretIf(bool(s), BarrierSecretUnsealKey)
}

var (
	_ BarrierSecretCarrier = CreateDatabaseRequest{}
	_ BarrierSecretCarrier = UpdateDatabaseBarrierRequest{}
	_ BarrierSecretCarrier = (*DatabaseBarrierConfig)(nil)
	_ BarrierSecretCarrier = (*BarrierInitializeRequest)(nil)
	_ BarrierSecretCarrier = (*BarrierUnsealRequest)(nil)
	_ BarrierSecretCarrier = (*BarrierChangePassphraseRequest)(nil)
	_ BarrierSecretCarrier = (*BarrierInitializeShamirRequest)(nil)
	_ BarrierSecretCarrier = (*BarrierEnableEncryptionRequest)(nil)
	_ BarrierSecretCarrier = (*BarrierGenerateRecoveryKeysRequest)(nil)
	_ BarrierSecretCarrier = (*QuorumSubmitRequest)(nil)
	_ BarrierSecretCarrier = (*RekeyStartRequest)(nil)
	_ BarrierSecretCarrier = (*OfficerRevokeRequest)(nil)
	_ BarrierSecretCarrier = (*CeremonyInitializeRequest)(nil)
	_ BarrierSecretCarrier = (*SignCSRInitRequest)(nil)
	_ BarrierSecretCarrier = (*TenantBarrierInitRequest)(nil)
	_ BarrierSecretCarrier = (*TenantBarrierUnsealRequest)(nil)
	_ BarrierSecretCarrier = (*TenantQuorumSubmitRequest)(nil)
	_ BarrierSecretCarrier = (*CreateTenantRequest)(nil)
	_ BarrierSecretCarrier = (*DatabaseQuorumSubmitRequest)(nil)
)
