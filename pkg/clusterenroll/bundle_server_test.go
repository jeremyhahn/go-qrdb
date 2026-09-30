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

// An SPKI-pinned HTTPS bootstrap server for tests. The SPKI bootstrapper
// returns the served body verbatim, which is what lets a test stand in for a
// member serving any bundle shape -- including the CA-key bundle an older
// release serves, which a node on this release must refuse.

package clusterenroll

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bundleServer serves bundle over TLS at the path the SPKI
// bootstrapper fetches, and returns the bootstrap options that reach it
// (including the SPKI pin of its own serving certificate).
func bundleServer(t *testing.T, bundle []byte) BootstrapOptions {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(bundle)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	leaf := srv.Certificate()
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)

	return BootstrapOptions{
		SPKIPin:    hex.EncodeToString(sum[:]),
		ServerAddr: strings.TrimPrefix(srv.URL, "https://"),
	}
}
