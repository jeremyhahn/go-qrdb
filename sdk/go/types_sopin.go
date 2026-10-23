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

package sdk

import "github.com/jeremyhahn/go-qrdb/sdk/go/transport"

// ---------------------------------------------------------------------------
// SO dual factor
// ---------------------------------------------------------------------------

// WithSOPIN returns a context that presents pin as the SO dual-factor PIN on
// every call made with it, on every network transport, and a release function
// that destroys the SDK's copy of the PIN, which it holds sealed in protected
// memory. The error is a protected-memory failure sealing it. Scope the
// context to the SO operation and defer release:
//
//	ctx, release, err := sdk.WithSOPIN(ctx, pin)
//	if err != nil {
//		return err
//	}
//	defer release()
//	_, err = client.Barrier().BarrierRekeyStart(ctx, req)
//
// REST and MCP send it in the X-QRDB-SO-PIN header, gRPC and the Unix socket
// in the x-qrdb-so-pin metadata entry, QUIC in the request message's so_pin
// field. Every network transport also fills the so_pin field of a request
// that has one from the bound PIN, so SOPin never needs setting by hand. The
// embedded transport has no SO gate and ignores it. The PIN is
// sent only over TLS and, on REST and MCP, only to the configured host on
// every hop: otherwise the call is refused with SOPINRequiresTLSError or
// SOPINForeignHostError before anything is sent. The PIN is
// sealed in protected memory and never logged, and the plaintext copies the
// transports open are wiped once written; HTTP header and gRPC metadata
// values are Go strings, which cannot be wiped. (Aliases sdk/go/transport.WithSOPIN.)
var WithSOPIN = transport.WithSOPIN

// SOPINRequiresTLSError is returned when a call made with a context bound by
// WithSOPIN would travel over a connection that is not TLS -- a plaintext
// gRPC or Unix socket connection, or an http hop on REST or MCP, redirects
// included. It is refused before anything is sent.
// (Aliases sdk/go/transport.SOPINRequiresTLSError.)
type SOPINRequiresTLSError = transport.SOPINRequiresTLSError

// SOPINForeignHostError is returned when a REST or MCP call made with a
// context bound by WithSOPIN is redirected to a host other than the
// configured one. It is refused before anything is sent.
// (Aliases sdk/go/transport.SOPINForeignHostError.)
type SOPINForeignHostError = transport.SOPINForeignHostError

// SOPINConflictError is returned by every network transport when a request's
// own SOPin and the PIN bound by WithSOPIN are both set and differ: the
// request's so_pin field carries one PIN.
// (Aliases sdk/go/transport.SOPINConflictError.)
type SOPINConflictError = transport.SOPINConflictError

// BarrierSecretRequiresTLSError is returned when a request carrying a barrier
// secret in its body -- an init secret, unseal key, passphrase, Shamir share
// or user PIN -- would travel over a connection that is not TLS: a plaintext
// gRPC or Unix socket connection, or an http hop on REST or MCP, redirects
// included. It is refused before anything is sent. Its Kind names the secret,
// never its value. A request's own SOPin is refused with
// SOPINRequiresTLSError instead. (Aliases
// sdk/go/transport.BarrierSecretRequiresTLSError.)
type BarrierSecretRequiresTLSError = transport.BarrierSecretRequiresTLSError

// BarrierSecretForeignHostError is returned when a REST or MCP request
// carrying a barrier secret in its body is redirected to a host other than the
// one it was sent to. The redirected request is refused before it is sent, so
// the secret never reaches that host; its fields name the transport, the kind
// of secret and both hosts, never the secret. A request's own SOPin is refused
// with SOPINForeignHostError instead. (Aliases
// sdk/go/transport.BarrierSecretForeignHostError.)
type BarrierSecretForeignHostError = transport.BarrierSecretForeignHostError

// BarrierSecretKind is the Kind of a BarrierSecretRequiresTLSError.
// (Aliases sdk/go/transport.BarrierSecretKind.)
type BarrierSecretKind = transport.BarrierSecretKind

// The kinds of barrier secret a BarrierSecretRequiresTLSError names.
// (Alias the sdk/go/transport constants of the same names.)
const (
	BarrierSecretInit       = transport.BarrierSecretInit
	BarrierSecretUnsealKey  = transport.BarrierSecretUnsealKey
	BarrierSecretPassphrase = transport.BarrierSecretPassphrase
	BarrierSecretShare      = transport.BarrierSecretShare
	BarrierSecretUserPIN    = transport.BarrierSecretUserPIN
)
