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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"testing"
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// injectFakeTLS — helper: sets r.TLS with a self-signed cert so that
// restCertThumbprint can compute a thumbprint without returning an error.
// ---------------------------------------------------------------------------

func injectFakeTLS(r *http.Request) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("injectFakeTLS: generate key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-officer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("injectFakeTLS: create cert: " + err.Error())
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic("injectFakeTLS: parse cert: " + err.Error())
	}
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
}

// ---------------------------------------------------------------------------
// mapHTTPError — exhaustive table test
// ---------------------------------------------------------------------------

func TestMapBarrierCeremonyHTTPError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"CertThumbprintMismatch", ceremony.ErrCertThumbprintMismatch, http.StatusForbidden},
		{"OfficerNotFound", ceremony.ErrOfficerNotFound, http.StatusNotFound},
		{"ShareAlreadyClaimed", ceremony.ErrShareAlreadyClaimed, http.StatusConflict},
		{"NoPendingShare", ceremony.ErrNoPendingShare, http.StatusNotFound},
		{"ShareExpired", ceremony.ErrShareExpired, http.StatusGone},
		{"InsufficientOfficers", ceremony.ErrInsufficientOfficers, http.StatusUnprocessableEntity},
		{"ShamirBarrierRequiresQuorum", seal.ErrShamirBarrierRequiresQuorum, http.StatusUnprocessableEntity},
		{"ShamirUnsealRequiresQuorum", seal.ErrShamirUnsealRequiresQuorum, http.StatusUnprocessableEntity},
		{"UnsealKeyMismatch", seal.ErrUnsealKeyMismatch, http.StatusForbidden},
		{"DecryptionFailed", seal.ErrDecryptionFailed, http.StatusForbidden},
		{"WrappedUnsealDecryption", &quicraftseal.UnsealError{Strategy: "passphrase", Err: quicraftseal.ErrDecryptionFailed}, http.StatusForbidden},
		{"WrappedRootKeyCorrupt", seal.ErrWrappedRootKeyCorrupt, http.StatusInternalServerError},
		{"AlreadyUnsealed", seal.ErrAlreadyUnsealed, http.StatusConflict},
		{"AlreadyInitialized", seal.ErrAlreadyInitialized, http.StatusConflict},
		{"NotInitialized", seal.ErrNotInitialized, http.StatusConflict},
		{"InvalidCredentials", seal.ErrInvalidCredentials, http.StatusForbidden},
		{"CeremonyNotConfigured", service.ErrCeremonyNotConfigured, http.StatusUnprocessableEntity},
		{"GenericError", errSentinel, http.StatusInternalServerError},
		{"ServiceErrorFallthrough", &service.KeyNotFoundError{Key: "k"}, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.err)
			if got != tc.wantCode {
				t.Fatalf("mapHTTPError(%v) = %d, want %d", tc.err, got, tc.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// System barrier SO handlers
// ---------------------------------------------------------------------------

// handleBarrierShareClaimBegin — missing peer cert path (no TLS on request).

// handleBarrierShareClaimComplete tests.

// handleBarrierShamirSealVote tests.

// Recovery officer register tests.
