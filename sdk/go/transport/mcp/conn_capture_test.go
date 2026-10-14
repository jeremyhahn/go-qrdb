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

package mcp

import (
	"crypto/tls"
	"errors"
	"net/http"
	"testing"
)

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// roundTrip sends req through c and closes the answer's body, so a test
// reads only what c recorded about the connection.
func roundTrip(c *connCapture, req *http.Request) error {
	resp, err := c.RoundTrip(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestConnCapture_RecordsTheAnsweringConnection(t *testing.T) {
	state := &tls.ConnectionState{Version: tls.VersionTLS13, ServerName: "db.example.com"}
	c := &connCapture{next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, TLS: state}, nil
	})}
	if c.TLSState() != nil {
		t.Fatal("TLSState before any request must be nil")
	}

	req, _ := http.NewRequest(http.MethodPost, "https://db.example.com/mcp", http.NoBody)
	if err := roundTrip(c, req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if c.TLSState() != state {
		t.Fatalf("TLSState = %+v; want the answering connection's state", c.TLSState())
	}
}

func TestConnCapture_KeepsTheLastTLSStateOnFailureAndPlaintext(t *testing.T) {
	state := &tls.ConnectionState{Version: tls.VersionTLS13}
	fail := errors.New("dial refused")
	answers := []func() (*http.Response, error){
		func() (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, TLS: state}, nil
		},
		func() (*http.Response, error) { return nil, fail },
		func() (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		},
	}
	next := 0
	c := &connCapture{next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		answer := answers[next]
		next++
		return answer()
	})}

	req, _ := http.NewRequest(http.MethodPost, "https://db.example.com/mcp", http.NoBody)
	if err := roundTrip(c, req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if err := roundTrip(c, req); !errors.Is(err, fail) {
		t.Fatalf("RoundTrip err = %v; want the underlying failure", err)
	}
	if err := roundTrip(c, req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if c.TLSState() != state {
		t.Fatal("a failed or plaintext round trip must not overwrite the recorded TLS state")
	}
}
