// Copyright (c) 2026 Jeremy Hahn
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

package middleware

import (
	"errors"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// AuthFailureClass is WHAT KIND of failure an authentication or authorization
// error represents, independent of any transport.
//
// It exists because the answer was being spelled out once per transport and
// then, on the tenant-barrier surface, not at all: eleven REST call sites and
// seven gRPC ones hard-coded "permission denied" for every error the
// authorization gate could return. That was survivable while
// *StoreUnavailableError almost never escaped those gates -- and stopped being
// survivable with go-qrdb#335, because *audit.GateError escapes them RELIABLY (the
// bootstrap-window fallback's own audit write fails for the same reason the
// first one did). An operator recovering a node with a full audit disk would
// have been told "you are not permitted" by the unseal endpoint.
//
// The middleware owns WHAT KIND OF FAILURE THIS IS. Each transport owns HOW IT
// SAYS THAT on its own wire. Splitting it the other way is what let four
// transports drift.
type AuthFailureClass uint8

const (
	// AuthFailureUnknown is an error this package does not recognise. Every
	// mapper takes the caller's own fallback for it rather than guessing,
	// because the right guess differs by call site: an authentication
	// middleware that cannot classify a failure means "unauthenticated",
	// while an authorization gate running AFTER authentication means
	// "unauthorized". Collapsing those was the one way this refactor could
	// have introduced a regression.
	AuthFailureUnknown AuthFailureClass = iota

	// AuthFailureUnauthenticated -- the credential is missing, malformed,
	// expired or revoked. Present a credential. 401 / Unauthenticated.
	AuthFailureUnauthenticated

	// AuthFailureUnauthorized -- the credential is good and the grant is
	// absent. Retrying is futile. 403 / PermissionDenied.
	AuthFailureUnauthorized

	// AuthFailureUnavailable -- NEITHER the credential nor the grant failed.
	// This node could not answer: it could not record the decision
	// (*audit.GateError), could not read the user store (*StoreUnavailableError),
	// or could not read revocation state (*auth.RevocationCheckError).
	// RETRY IS THE HONEST INSTRUCTION. 503 / Unavailable.
	AuthFailureUnavailable

	// AuthFailureRateLimited -- credential and grant are both fine and the
	// caller is over its quota. Back off and retry.
	// 429 / ResourceExhausted.
	AuthFailureRateLimited
)

// String returns a short name for logs and test failure messages.
func (c AuthFailureClass) String() string {
	switch c {
	case AuthFailureUnauthenticated:
		return "unauthenticated"
	case AuthFailureUnauthorized:
		return "unauthorized"
	case AuthFailureUnavailable:
		return "unavailable"
	case AuthFailureRateLimited:
		return "rate-limited"
	default:
		return "unknown"
	}
}

// Retryable reports whether a caller should try the same request again.
//
// It is the question a transport with no status code -- MCP, whose tool
// results carry text -- still has to answer, so that its message does not tell
// an operator "authorization denied" about a full audit disk.
func (c AuthFailureClass) Retryable() bool {
	return c == AuthFailureUnavailable || c == AuthFailureRateLimited
}

// authFailureRules is the ORDERED classification chain. Order is significant
// and reproduces, exactly, the sequence mapHTTPAuthError and mapGRPCAuthError
// each used to spell out separately -- so neither changes behaviour for any
// error either already recognised.
//
// A slice rather than a map keyed by type: errors.As has to run against a
// typed target, and the chain is first-match-wins, which a map cannot express.
// Adding a class here reaches all four transports at once, which is the point.
var authFailureRules = []struct {
	name  string
	match func(error) (AuthFailureClass, bool)
}{
	{"auth.AuthError", func(err error) (AuthFailureClass, bool) {
		var authErr *auth.AuthError
		if !errors.As(err, &authErr) {
			return AuthFailureUnknown, false
		}
		switch authErr.Code {
		case "unauthorized":
			return AuthFailureUnauthorized, true
		case "unauthenticated", "no_peer_cert", "invalid_cert", "cert_revoked":
			return AuthFailureUnauthenticated, true
		}
		// An AuthError carrying some other code falls THROUGH to the rules
		// below, exactly as it did before. It is not classified here.
		return AuthFailureUnknown, false
	}},
	{"StoreUnavailableError", func(err error) (AuthFailureClass, bool) {
		var e *StoreUnavailableError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// The audit plane failed, not the caller. See audit.GateError: the
	// credential is valid and the grant -- or the SO PIN -- may be too. ONE
	// rule covers every gate, because go-qrdb#445 made them one type; a second
	// error type for the SO factor would have meant a second rule here and a
	// second mapping in each of the four transports.
	{"audit.GateError", func(err error) (AuthFailureClass, bool) {
		var e *audit.GateError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	// The certificate may be perfectly good and this node could not read
	// revocation state. Telling a legitimate caller its credential is bad
	// would send it to re-enrol; telling it to retry is the truth.
	{"auth.RevocationCheckError", func(err error) (AuthFailureClass, bool) {
		var e *auth.RevocationCheckError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	{"UserDisabledError", func(err error) (AuthFailureClass, bool) {
		var e *UserDisabledError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	// The credential is valid and the account is fine -- this WAY in is not
	// one the account may use, and retrying it will never work.
	{"AuthMethodNotPermittedError", func(err error) (AuthFailureClass, bool) {
		var e *AuthMethodNotPermittedError
		return AuthFailureUnauthorized, errors.As(err, &e)
	}},
	// The certificate is not this account's credential at all, so the caller
	// never established who it is: 401, not 403. Contrast the rule immediately
	// above, where the credential IS the account's -- there, retrying is
	// futile and the answer is "forbidden"; here, presenting the certificate
	// the account actually enrolled works.
	{"CertBindingNotPermittedError", func(err error) (AuthFailureClass, bool) {
		var e *CertBindingNotPermittedError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	{"AuthChainError", func(err error) (AuthFailureClass, bool) {
		var e *AuthChainError
		return AuthFailureUnauthenticated, errors.As(err, &e)
	}},
	// The node could not establish WHICH tenant a revocation targets, so it
	// could not decide. 503, not 403: the caller's grant was never consulted,
	// and answering "forbidden" would send an entitled operator to fix a
	// permission that is not the problem.
	{"CertSerialUnresolvableError", func(err error) (AuthFailureClass, bool) {
		var e *CertSerialUnresolvableError
		return AuthFailureUnavailable, errors.As(err, &e)
	}},
	{"RateLimitedError", func(err error) (AuthFailureClass, bool) {
		var e *RateLimitedError
		return AuthFailureRateLimited, errors.As(err, &e)
	}},
}

// ClassifyAuthError reports what kind of failure err represents, or
// AuthFailureUnknown when this package does not recognise it.
//
// A nil error is AuthFailureUnknown: there is no failure to classify, and a
// caller that reaches a mapper with nil has a bug this function must not
// paper over by inventing a success class.
func ClassifyAuthError(err error) AuthFailureClass {
	if err == nil {
		return AuthFailureUnknown
	}
	for _, rule := range authFailureRules {
		if class, ok := rule.match(err); ok {
			return class
		}
	}
	return AuthFailureUnknown
}

// httpStatusByClass maps a classified failure onto its HTTP status.
var httpStatusByClass = map[AuthFailureClass]int{
	AuthFailureUnauthenticated: http.StatusUnauthorized,
	AuthFailureUnauthorized:    http.StatusForbidden,
	AuthFailureUnavailable:     http.StatusServiceUnavailable,
	AuthFailureRateLimited:     http.StatusTooManyRequests,
}

// HTTPStatus returns the HTTP status err means, or fallback when this package
// does not recognise it.
//
// THE FALLBACK IS THE CALLER'S TO CHOOSE, and the two callers choose
// differently on purpose:
//
//   - The REST auth middleware passes http.StatusUnauthorized. It runs
//     authentication as well as authorization, so an unrecognised failure
//     there most likely means the caller never established who it was.
//   - A handler-level authorization gate -- the tenant-barrier surface --
//     passes http.StatusForbidden. Authentication has already succeeded by
//     the time those run, so 401 would be a lie and a regression on the
//     status those sites answered before this function existed.
//
// Hard-coding one default for both is what would have turned this fix into a
// silent behaviour change on every unrecognised error.
func HTTPStatus(err error, fallback int) int {
	if code, ok := httpStatusByClass[ClassifyAuthError(err)]; ok {
		return code
	}
	return fallback
}

// grpcCodeByClass maps a classified failure onto its gRPC code.
//
// ResourceExhausted for the rate-limited class, not PermissionDenied: it is
// the standard signal a gRPC client library maps to "back off and retry"
// rather than "re-authenticate" or "do not retry at all".
var grpcCodeByClass = map[AuthFailureClass]codes.Code{
	AuthFailureUnauthenticated: codes.Unauthenticated,
	AuthFailureUnauthorized:    codes.PermissionDenied,
	AuthFailureUnavailable:     codes.Unavailable,
	AuthFailureRateLimited:     codes.ResourceExhausted,
}

// GRPCError converts err to a gRPC status error, using fallback for anything
// this package does not recognise. See HTTPStatus for why the fallback is the
// caller's.
func GRPCError(err error, fallback codes.Code) error {
	if err == nil {
		return nil
	}
	if code, ok := grpcCodeByClass[ClassifyAuthError(err)]; ok {
		return status.Error(code, err.Error())
	}
	return status.Error(fallback, err.Error())
}
