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

package rest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestBarrierSecret_RequiresTLS: a barrier secret is never sent to an http://
// server, is sent to an https:// one, and a request without one is still sent
// over http://.
func TestBarrierSecret_RequiresTLS(t *testing.T) {
	var plainReached, tlsReached atomic.Int64
	plain := httptest.NewServer(secretCounter(&plainReached))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(secretCounter(&tlsReached))
	t.Cleanup(secure.Close)
	sotest.RunBarrierSecretTLS(t, "rest",
		sotest.SecretTarget{Client: connectREST(t, plain.URL, nil), Reached: counted(&plainReached)},
		sotest.SecretTarget{Client: connectREST(t, secure.URL, trusting(secure)), Reached: counted(&tlsReached)},
	)
}

// TestBarrierSecret_PlaintextRedirectIsRefused: an https:// server that
// redirects a barrier secret to http:// does not get it sent there.
func TestBarrierSecret_PlaintextRedirectIsRefused(t *testing.T) {
	var plainReached atomic.Int64
	plain := httptest.NewServer(secretCounter(&plainReached))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// #nosec G710 -- the open redirect is the fixture: it sends the client to this test's own plaintext server to prove the secret does not follow.
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(secure.Close)
	tr := connectREST(t, secure.URL, trusting(secure))

	err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("unseal key")})
	var tlsErr *transport.BarrierSecretRequiresTLSError
	if !errors.As(err, &tlsErr) || tlsErr.Kind != transport.BarrierSecretUnsealKey {
		t.Fatalf("err = %v, want *transport.BarrierSecretRequiresTLSError for the unseal key", err)
	}
	if n := plainReached.Load(); n != 0 {
		t.Fatalf("the unseal key followed a redirect to http:// (%d requests)", n)
	}
}

// TestBarrierSecret_ForeignHostRedirectIsRefused: an https:// server that
// answers a barrier secret with a 307 or 308 to another https:// host does not
// get the secret resent there.
func TestBarrierSecret_ForeignHostRedirectIsRefused(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			foreign := &bodyRecorder{}
			foreignSrv := httptest.NewTLSServer(foreign)
			t.Cleanup(foreignSrv.Close)
			origin := httptest.NewTLSServer(redirectTo(foreignSrv.URL, status))
			t.Cleanup(origin.Close)
			// One trust store for both: httptest servers share a certificate.
			tr := connectREST(t, origin.URL, trusting(origin))

			err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("unseal key")})
			var hostErr *transport.BarrierSecretForeignHostError
			if !errors.As(err, &hostErr) {
				t.Fatalf("err = %v, want *transport.BarrierSecretForeignHostError", err)
			}
			want := transport.BarrierSecretForeignHostError{
				Transport:    "rest",
				Kind:         transport.BarrierSecretUnsealKey,
				OriginalHost: transport.ConfiguredHost(origin.URL),
				RedirectHost: transport.ConfiguredHost(foreignSrv.URL),
			}
			if *hostErr != want {
				t.Fatalf("err = %+v, want %+v", *hostErr, want)
			}
			if n, body := foreign.seen(); n != 0 || body != 0 {
				t.Fatalf("the foreign host saw %d requests and %d body bytes, want none", n, body)
			}
		})
	}
}

// TestBarrierSecret_SameHostRedirectIsFollowed: a 307 or 308 to another path
// on the same host is followed, and the secret arrives there.
func TestBarrierSecret_SameHostRedirectIsFollowed(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			moved := &bodyRecorder{}
			mux := http.NewServeMux()
			mux.Handle("/moved/", moved)
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(http.StatusOK)
					return
				}
				http.Redirect(w, r, "/moved"+r.URL.Path, status)
			})
			srv := httptest.NewTLSServer(mux)
			t.Cleanup(srv.Close)
			tr := connectREST(t, srv.URL, trusting(srv))

			if err := tr.BarrierUnseal(context.Background(), &transport.BarrierUnsealRequest{Secret: []byte("unseal key")}); err != nil {
				t.Fatalf("BarrierUnseal through a same-host redirect: %v", err)
			}
			if n, body := moved.seen(); n != 1 || body == 0 {
				t.Fatalf("the redirect target saw %d requests and %d body bytes, want 1 with a body", n, body)
			}
		})
	}
}

// redirectTo answers /health, so Connect succeeds, and redirects every other
// request to the same path on target with status.
func redirectTo(target string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// #nosec G710 -- the open redirect is the fixture: it sends the client to this test's own second httptest server to prove the secret does not follow.
		http.Redirect(w, r, target+r.URL.Path, status)
	})
}

// bodyRecorder counts the requests it is sent and the body bytes they carry,
// answering an empty JSON object.
type bodyRecorder struct {
	requests  atomic.Int64
	bodyBytes atomic.Int64
}

func (b *bodyRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n, _ := io.Copy(io.Discard, r.Body)
	b.requests.Add(1)
	b.bodyBytes.Add(n)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

func (b *bodyRecorder) seen() (requests, bodyBytes int64) {
	return b.requests.Load(), b.bodyBytes.Load()
}

// secretCounter answers /health, so Connect succeeds, and counts every other
// request, answering an empty JSON object.
func secretCounter(reached *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
}

func counted(reached *atomic.Int64) func() int {
	return func() int { return int(reached.Load()) }
}
