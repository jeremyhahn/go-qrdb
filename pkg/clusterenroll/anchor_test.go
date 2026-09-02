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
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// anchorTestCA generates a fresh CA and returns its cert and key PEM.
func anchorTestCA(t *testing.T) (caCertPEM, caKeyPEM []byte) {
	t.Helper()
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	return caCertPEM, caKeyPEM
}

// ---------------------------------------------------------------------------
// ValidateAnchorBundle
// ---------------------------------------------------------------------------

func TestValidateAnchorBundle_CertOnly(t *testing.T) {
	caCertPEM, _ := anchorTestCA(t)
	if err := ValidateAnchorBundle(caCertPEM); err != nil {
		t.Fatalf("expected cert-only bundle to validate, got: %v", err)
	}
}

func TestValidateAnchorBundle_RejectsPrivateKey(t *testing.T) {
	caCertPEM, caKeyPEM := anchorTestCA(t)
	bundle := append(append([]byte{}, caCertPEM...), caKeyPEM...)

	err := ValidateAnchorBundle(bundle)
	if err == nil {
		t.Fatal("expected bundle with private key to be rejected")
	}
	var anchorErr *AnchorError
	if !errors.As(err, &anchorErr) {
		t.Fatalf("expected *AnchorError, got %T: %v", err, err)
	}
	if !strings.Contains(anchorErr.Error(), "private-key") {
		t.Errorf("unexpected error message: %v", anchorErr)
	}
}

func TestValidateAnchorBundle_RejectsNoCertificate(t *testing.T) {
	err := ValidateAnchorBundle([]byte("not pem at all"))
	if err == nil {
		t.Fatal("expected bundle without certificates to be rejected")
	}
	var anchorErr *AnchorError
	if !errors.As(err, &anchorErr) {
		t.Fatalf("expected *AnchorError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// StartAnchorAdvertiser
// ---------------------------------------------------------------------------

func TestStartAnchorAdvertiser_RejectsKeyBearingBundle(t *testing.T) {
	caCertPEM, caKeyPEM := anchorTestCA(t)
	bundle := append(append([]byte{}, caCertPEM...), caKeyPEM...)

	_, err := StartAnchorAdvertiser(bundle, AdvertiseOptions{NoisePort: 63106, NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err == nil {
		t.Fatal("expected anchor advertiser to reject a key-bearing bundle")
	}
	var anchorErr *AnchorError
	if !errors.As(err, &anchorErr) {
		t.Fatalf("expected *AnchorError, got %T: %v", err, err)
	}
}

func TestStartAnchorAdvertiser_ServesAnchorRoundTrip(t *testing.T) {
	caCertPEM, _ := anchorTestCA(t)
	dir := t.TempDir()

	adv, err := StartAnchorAdvertiser(caCertPEM, AdvertiseOptions{NoisePort: 63107, NoiseHost: testBindHost}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser: %v", err)
	}
	defer adv.Stop()

	if adv.Addr() == "" {
		t.Fatal("expected a listen address")
	}
	if len(adv.NoisePublicKey()) == 0 {
		t.Fatal("expected a Noise public key")
	}

	// Joiner side: fetch the anchor over the Noise NK channel.
	opts := &Options{
		Bootstrap: BootstrapOptions{
			NoiseKey:  hex.EncodeToString(adv.NoisePublicKey()),
			NoiseAddr: "127.0.0.1:63107",
		},
	}
	fetched, err := FetchAnchor(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("FetchAnchor: %v", err)
	}
	if string(fetched) != string(caCertPEM) {
		t.Error("fetched anchor does not match the advertised anchor")
	}
}

// ---------------------------------------------------------------------------
// FetchAnchor
// ---------------------------------------------------------------------------

func TestFetchAnchor_NoBootstrapFlags(t *testing.T) {
	_, err := FetchAnchor(context.Background(), &Options{}, discardLogger())
	if err == nil {
		t.Fatal("expected error when no bootstrap method is configured")
	}
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("expected *FetchError, got %T: %v", err, err)
	}
}

func TestFetchAnchor_SharedKeyModeServerYieldsCertOnly(t *testing.T) {
	// The truststrap Noise NK protocol transmits only CERTIFICATE blocks (the
	// server-side handler filters the bundle), so even an advertiser holding
	// the CA key (StartAdvertiser) can only ever deliver the certificate over
	// Noise. FetchAnchor therefore succeeds and yields a cert-only anchor.
	// ValidateAnchorBundle's private-key rejection guards the other transport
	// methods (SPKI/DANE/Direct HTTPS), which relay the provider bundle as-is.
	caCertPEM, caKeyPEM := anchorTestCA(t)
	mat := &clustertls.Material{CACertPEM: caCertPEM, CAKeyPEM: caKeyPEM}

	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: 63108, NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser: %v", err)
	}
	defer adv.Stop()

	opts := &Options{
		Bootstrap: BootstrapOptions{
			NoiseKey:  hex.EncodeToString(adv.NoisePublicKey()),
			NoiseAddr: "127.0.0.1:63108",
		},
	}
	fetched, err := FetchAnchor(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("FetchAnchor: %v", err)
	}
	if err := ValidateAnchorBundle(fetched); err != nil {
		t.Fatalf("fetched bundle failed anchor validation: %v", err)
	}
	if string(fetched) == string(append(append([]byte{}, caCertPEM...), caKeyPEM...)) {
		t.Fatal("fetched bundle must not contain the CA private key")
	}
}
