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

package main

import "github.com/jeremyhahn/go-qrdb/pkg/clustertls"

// generateCATLS is a test fixture: an ephemeral ECDSA P-256 CA and a leaf
// certificate signed by it, assembled into a TLSBundle the way the daemon
// assembles cluster material. When nodeURI is provided it is embedded as a
// URI SAN. The daemon itself resolves its TLS through resolveTLS.
func generateCATLS(nodeURI ...string) (*TLSBundle, error) {
	m, err := clustertls.GenerateCABundle(nodeURI...)
	if err != nil {
		return nil, err
	}
	return tlsBundleFromMaterial(m)
}
