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

package mcphttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// bearerTransport sets a bearer token on every request it sends.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

// TestClient_NeverFollowsRedirectToAnotherOrigin covers GO-2026-6911
// (ported from go-xkms d8bf196dd3): an MCP server that answers with a
// Location header must not make an mcp-go client on this transport send a
// request, or its bearer token, to the origin the header names. The
// transport itself never reads Location: the client's refusing
// CheckRedirect stops the hop, and the 3xx is a failure.
func TestClient_NeverFollowsRedirectToAnotherOrigin(t *testing.T) {
	errRefused := errors.New("redirect refused")
	for _, code := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var mu sync.Mutex
			var reached int
			var leaked []string
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				reached++
				if auth := r.Header.Get("Authorization"); auth != "" {
					leaked = append(leaked, auth)
				}
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target.URL+"/mcp")
				w.WriteHeader(code)
			}))
			defer origin.Close()

			hc := &http.Client{
				Transport:     bearerTransport{token: "tok-redirect"},
				CheckRedirect: func(*http.Request, []*http.Request) error { return errRefused },
			}
			c := mcpclient.NewClient(New(origin.URL+"/mcp", hc))
			if err := c.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err == nil {
				t.Fatal("Initialize succeeded; a redirect is not an MCP response")
			}
			if err := c.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if reached != 0 || len(leaked) != 0 {
				t.Fatalf("the redirect target received %d requests, Authorization %q", reached, leaked)
			}
		})
	}
}
