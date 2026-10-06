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

package clusterenroll

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-truststrap/pkg/spkipin"
)

// JoinHTTPPath is the REST path of the SPKI/Direct member-side CSR-sign
// route (pkg/api/rest's handleClusterJoin), registered on the regular
// (post-unseal) admin HTTPS listener alongside /v1/ca/bootstrap. It is the
// canonical wire contract both the client here and the server in
// pkg/api/rest agree on; pkg/api/rest imports this constant rather than
// restating it.
const JoinHTTPPath = "/v1/enroll/join"

// joinHTTPMaxResponseSize bounds the response body read, mirroring
// truststrap's own bundle-fetch limits.
const joinHTTPMaxResponseSize = 1 << 20

// JoinHTTPClientOptions configures a joiner's CSR submission over the
// SPKI-pinned or Direct HTTPS join route -- the same admin HTTPS listener
// and trust establishment /v1/ca/bootstrap already uses, with a POST body
// instead of a GET.
type JoinHTTPClientOptions struct {
	// ServerURL is the base URL of an admitted member's admin HTTPS server
	// (e.g. "https://10.0.0.2:8443"). JoinHTTPPath is appended to it.
	ServerURL string

	// SPKIPin is the hex SHA-256 SPKI pin of that server's certificate.
	// Empty selects Direct mode: the system trust store verifies the
	// server, exactly as truststrap's DirectBootstrapper does for the
	// anchor fetch.
	SPKIPin string

	// Token is the join-admission secret. Empty submits the CSR into the
	// signing member's replicated approval queue instead (see JoinCSRHTTP).
	Token string

	// ReplicaID is this node's own Raft replica ID, carried so the signing
	// member can publish a pre-membership feature advertisement for it (see
	// FeatureAdvertiser). Zero skips advertising.
	ReplicaID uint64

	// Features names the cluster features this node's binary implements
	// (featuregate.SupportedNames()). The signing member filters this to
	// names it knows before advertising anything.
	Features []string

	// Timeout bounds the request. Zero uses DefaultTimeout.
	Timeout time.Duration
}

func (o *JoinHTTPClientOptions) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

// JoinCSRHTTP submits csrDER to an admitted member's SPKI/Direct join route
// and returns the signed leaf. It is the HTTPS counterpart of JoinCSR (the
// Noise_NK transport): same JoinRequest/JoinResponse wire contract, same
// HandleJoinRequest core on the server side, a plain HTTPS POST instead of
// a Noise-encrypted exchange. Trust in the server is established exactly as
// the anchor fetch establishes it for this method (SPKI pin, or the system
// trust store for Direct) -- never skipped.
func JoinCSRHTTP(ctx context.Context, opts *JoinHTTPClientOptions, csrDER []byte, nodeURI string) ([]byte, error) {
	client, err := buildJoinHTTPClient(opts)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()

	req := JoinRequest{Token: opts.Token, CSRDER: csrDER, NodeURI: nodeURI, ReplicaID: opts.ReplicaID, Features: opts.Features}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &FetchError{Method: "join-http", Err: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.ServerURL+JoinHTTPPath, bytes.NewReader(body))
	if err != nil {
		return nil, &FetchError{Method: "join-http", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq) // #nosec G704 -- ServerURL is operator-configured, not user input
	if err != nil {
		return nil, &FetchError{Method: "join-http", Err: err}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, joinHTTPMaxResponseSize))
	if err != nil {
		return nil, &FetchError{Method: "join-http", Err: err}
	}

	var jr JoinResponse
	if err := json.Unmarshal(respBody, &jr); err != nil {
		return nil, &FetchError{Method: "join-http", Err: err}
	}
	if jr.Error != "" {
		return nil, &JoinRefusedError{Reason: jr.Error}
	}
	if jr.Pending {
		return nil, &JoinPendingError{RequestID: jr.RequestID}
	}
	return jr.CertPEM, nil
}

// buildJoinHTTPClient builds an *http.Client trusting opts.ServerURL exactly
// as the matching anchor-fetch method would: SPKI-pinned when opts.SPKIPin
// is set, the system trust store (Direct) otherwise.
func buildJoinHTTPClient(opts *JoinHTTPClientOptions) (*http.Client, error) {
	if opts.SPKIPin == "" {
		return &http.Client{Timeout: opts.timeout()}, nil
	}
	tlsConfig, err := spkipin.NewPinnedTLSConfig(opts.SPKIPin)
	if err != nil {
		return nil, &FetchError{Method: "join-http", Err: err}
	}
	return &http.Client{
		Timeout:   opts.timeout(),
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
	}, nil
}
