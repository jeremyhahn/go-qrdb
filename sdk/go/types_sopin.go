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
// that wipes the SDK's copy of the PIN. Scope the context to the SO operation
// and defer release:
//
//	ctx, release := sdk.WithSOPIN(ctx, pin)
//	defer release()
//	_, err := client.Barrier().BarrierRekey(ctx, req)
//
// REST and MCP send it in the X-QRDB-SO-PIN header, gRPC and the Unix socket
// in the x-qrdb-so-pin metadata entry, QUIC in the request message's so_pin
// field. The embedded transport has no SO gate and ignores it. The PIN is
// copied, never logged, and the copies the transports make are wiped once
// written; HTTP header and gRPC metadata values are Go strings, which cannot
// be wiped. (Aliases sdk/go/transport.WithSOPIN.)
var WithSOPIN = transport.WithSOPIN

// SOPINConflictError is returned by the QUIC transport when a request's own
// SOPin and the PIN bound by WithSOPIN are both set and differ: QUIC carries
// one PIN, in one field. (Aliases sdk/go/transport.SOPINConflictError.)
type SOPINConflictError = transport.SOPINConflictError
