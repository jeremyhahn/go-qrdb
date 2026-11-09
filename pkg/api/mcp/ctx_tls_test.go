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

// ctxWithFakeTLS lives here, in a file with NO build constraint, because a
// helper's constraint must be a superset of every caller's (go-qrdb#178).
// Its caller today is tools_barrier_test.go, under codec_json && raft &&
// qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd.
//
// It previously sat in that file while tools_coverage_extra_test.go, under
// codec_json && raft alone, also called it, so pkg/api/mcp did not typecheck
// under "codec_json,raft" at all (go-qrdb#263). Unconstrained, it stays
// correct for a caller added under any tag set.
//
// Nothing here needs a build tag: the helper is standard library plus
// auth.WithTLSState, and auth is a production package with no tag gate. Its
// old constraint was an accident of where it was written, not a requirement.
package mcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ctxWithFakeTLS returns a context carrying a self-signed certificate as the
// mTLS peer certificate, for handlers that read the caller identity off the
// TLS state.
func ctxWithFakeTLS() context.Context {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("ctxWithFakeTLS: generate key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-officer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("ctxWithFakeTLS: create cert: " + err.Error())
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic("ctxWithFakeTLS: parse cert: " + err.Error())
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
	return auth.WithTLSState(context.Background(), state)
}
