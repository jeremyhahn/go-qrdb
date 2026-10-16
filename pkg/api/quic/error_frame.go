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

package quic

import (
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// internalErrorMessage is what a caller is told when the server cannot say
// more: a recovered handler panic, or an error frame that could not be built.
const internalErrorMessage = "internal server error"

// errorPayloadOf is err as an error frame body: its message, and the delay it
// names (errclass.RetryAfter), which is the same delay REST sends as
// Retry-After and gRPC as RetryInfo.
func errorPayloadOf(err error) wire.ErrorPayload {
	p := wire.ErrorPayload{Message: err.Error()}
	p.RetryAfter, p.HasRetryAfter = errclass.RetryAfter(err)
	if reason, md, ok := errclass.Refusal(err); ok {
		p.Info = &wire.ErrorInfo{Domain: replicaforward.ErrorDomain, Reason: reason, Metadata: md}
	}
	return p
}

// errorFrameBody encodes p as the body of an error frame whose status is st.
// Every non-OK frame this server writes has its body built here, so a client
// decodes one format whatever refused the request.
//
// Encoding cannot fail for a payload wire.EncodeErrorPayload accepts -- it
// repairs a message that is not valid UTF-8 -- but the error is not dropped:
// it is logged, and the caller is told the request failed inside the server,
// with the status that says so, rather than given an empty body.
func errorFrameBody(st uint16, p wire.ErrorPayload) (uint16, []byte) {
	body, err := wire.EncodeErrorPayload(st, p)
	if err == nil {
		return st, body
	}
	slog.Error("quic: error frame could not be encoded", "status", wire.StatusName(st), "error", err)
	body, err = wire.EncodeErrorPayload(wire.StatusError, wire.ErrorPayload{Message: internalErrorMessage})
	if err != nil {
		// A constant message with no detail always encodes; if even it does
		// not, the empty body is itself a valid, empty google.rpc.Status.
		slog.Error("quic: internal-error frame could not be encoded", "error", err)
		return wire.StatusError, nil
	}
	return wire.StatusError, body
}

// errorFrame is err as an error frame with status st.
func errorFrame(st uint16, err error) (uint16, []byte) {
	return errorFrameBody(st, errorPayloadOf(err))
}
