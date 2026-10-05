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

package lifecycle

import (
	"crypto/x509"
	"encoding/pem"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
)

// TrustPoolClusterCA decides what the dynamic client-auth trust
// pool's cluster-CA exemption (pkg/clienttrust.Pool.SetClusterCA) should
// resolve to, independent of ceremonyCA's own nil-ness (go-qrdb#591's
// review, B4 -- see its call site's own doc comment for why that
// independence matters). Returns (nil, nil) for the documented, supported
// "no cluster-CA concept at all" state (Pool.SetClusterCA's own doc
// comment), never an error, when neither case below applies.
//
//   - clusterCAProv non-nil (a self-issued cluster): wraps it in
//     ceremony.NewProviderCA, which resolves the signer and certificate
//     fresh from the provider on every call, so a later re-seal/re-unseal
//     or HSM failover behind it is reflected without re-wiring.
//   - clusterCAProv nil and explicitTLS true (BYO-TLS,
//     lifecycle.Infra.NoClusterCAKey): this node has no cluster CA key,
//     but the operator's own peer CA -- caCertPEM, the same anchor
//     node-to-node Raft trust uses -- is the cluster CA for IsClusterCA
//     and on-behalf verification too (owner ruling, go-qrdb#591). It is
//     wrapped in clienttrust.NewStaticClusterCA: static, not re-resolved,
//     because an operator-supplied file has no rotation-behind-a-provider
//     concept to reflect.
func TrustPoolClusterCA(clusterCAProv cakey.Provider, explicitTLS bool, caCertPEM []byte) (clienttrust.ClusterCA, error) {
	if clusterCAProv != nil {
		providerCA, err := ceremony.NewProviderCA(clusterCAProv)
		if err != nil {
			return nil, &TrustPoolClusterCAError{Stage: "cluster CA provider unavailable", Err: err}
		}
		return providerCA, nil
	}
	if !explicitTLS {
		return nil, nil
	}
	block, _ := pem.Decode(caCertPEM)
	if block == nil {
		return nil, &TrustPoolClusterCAError{Stage: "BYO-TLS CA certificate did not decode as PEM"}
	}
	operatorCACert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, &TrustPoolClusterCAError{Stage: "BYO-TLS CA certificate failed to parse", Err: err}
	}
	staticCA, err := clienttrust.NewStaticClusterCA(operatorCACert)
	if err != nil {
		return nil, &TrustPoolClusterCAError{Stage: "constructing the BYO-TLS static cluster CA", Err: err}
	}
	return staticCA, nil
}

// TrustPoolClusterCAError is returned by TrustPoolClusterCA when
// the dynamic trust pool's cluster-CA exemption cannot be resolved. It is
// never fatal to boot: the caller logs it and proceeds with no
// cluster-CA concept wired, same as the documented "SetClusterCA never
// called" state.
type TrustPoolClusterCAError struct {
	Stage string
	Err   error
}

// Error returns a human-readable description.
func (e *TrustPoolClusterCAError) Error() string {
	msg := "lifecycle: " + e.Stage
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *TrustPoolClusterCAError) Unwrap() error {
	return e.Err
}
