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

package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"time"
)

// testRequest and testBundle are this package's in-package twin of
// pkg/cert/certtest, which its own tests cannot import (certtest imports
// cert): a client that makes its own key and has the provider sign it.
type testRequest struct {
	Username string
	TenantID string
	TTL      time.Duration
}

type testBundle struct {
	CertPEM []byte
	KeyPEM  []byte
	Serial  string
}

func generateForTest(ctx context.Context, p CertProvider, req *testRequest) (*testBundle, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	var r *IssueRequest
	if req != nil {
		r = &IssueRequest{Username: req.Username, TenantID: req.TenantID, PublicKey: &key.PublicKey, TTL: req.TTL}
	}
	issued, err := p.IssueClientCert(ctx, r)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &testBundle{
		CertPEM: issued.CertPEM,
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}),
		Serial:  issued.Serial,
	}, nil
}
