// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clientlimit

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// HTTP wraps next so every request is admitted before next runs. A refused
// request gets 429 Too Many Requests with a Retry-After header in whole
// seconds (RFC 9110 §10.2.3, RFC 6585 §4) and the refusal as the JSON body
// every other qrdb HTTP error carries. On a nil Limiter HTTP returns next
// unchanged.
func (l *Limiter) HTTP(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := l.AdmitRemoteAddr(r.RemoteAddr, r.Header.Values); err != nil {
			WriteHTTPError(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admittedKey is the context key under which HTTPOnce records the Limiter
// that charged a request.
type admittedKey struct{}

// HTTPOnce wraps next as HTTP does and records, in the context of the
// request next receives, that l has charged it. A layer below next that
// shares l -- the gRPC interceptors behind the gRPC-Web translator -- then
// sees Admitted and does not charge the request a second time. On a nil
// Limiter HTTPOnce returns next unchanged.
func (l *Limiter) HTTPOnce(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return l.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), admittedKey{}, l)))
	}))
}

// Admitted reports whether l itself already charged the request ctx
// belongs to, through HTTPOnce. A mark left by another Limiter does not
// count, so a layer never skips a charge its own Limiter has not made.
func (l *Limiter) Admitted(ctx context.Context) bool {
	if l == nil {
		return false
	}
	charged, _ := ctx.Value(admittedKey{}).(*Limiter)
	return charged == l
}

// WriteHTTPError writes err, a refusal from Admit, as the 429 response HTTP
// writes. An error that is not a *RateLimitedError is written as 429 without
// a Retry-After header.
func WriteHTTPError(w http.ResponseWriter, err error) {
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		w.Header().Set("Retry-After", strconv.FormatInt(authmw.RetryAfterSeconds(rl.RetryAfter), 10))
	}
	authmw.WriteJSONError(w, http.StatusTooManyRequests, err.Error())
}
