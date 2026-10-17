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

// The cluster CA private key never leaves the node that holds it, in any
// form: not in an enrollment response, not in a Noise bundle, and not onto a
// joining node's disk. These tests pin each of those three.

package clusterenroll

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// assertNoPrivateKeyPEM fails when data carries any PEM block whose type
// names private-key material. It checks the raw bytes as well as every
// decodable block, so a key that is present but not well-formed PEM is
// still caught.
func assertNoPrivateKeyPEM(t *testing.T, what string, data []byte) {
	t.Helper()
	if bytes.Contains(data, []byte("PRIVATE KEY")) {
		t.Fatalf("%s carries private-key material:\n%s", what, data)
	}
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			t.Fatalf("%s carries a %s block", what, block.Type)
		}
	}
}

// legacyKeyBundle returns a CA certificate with the CA private key
// concatenated after it: the shape a member running an older release still
// serves. A joining node on this release must refuse it outright.
func legacyKeyBundle(t *testing.T) []byte {
	t.Helper()
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	return append(append([]byte{}, caCertPEM...), caKeyPEM...)
}

// assertNoEnrollmentMaterial fails when dir holds any file a join writes. A
// refused join must leave the data directory exactly as it found it: above
// all no ca-key.pem, and no role file that would send the next boot down the
// cached path.
func assertNoEnrollmentMaterial(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"ca-key.pem", "ca.pem", "node-cert.pem", "node-key.pem", RoleFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("a refused join persisted %s", name)
		}
	}
}

// TestJoinNeverPersistsACAKeyAnOlderMemberServes drives Join against a
// member that still serves the CA key (an older release). Even handed the
// key, the joining node must not adopt it: no leaf minted from it, and no
// ca-key.pem on disk.
func TestJoinNeverPersistsACAKeyAnOlderMemberServes(t *testing.T) {
	bootstrap := bundleServer(t, legacyKeyBundle(t))
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63030",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bootstrap,
	}

	mat, err := Join(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("Join must refuse: this release does not enroll from a distributed CA key")
	}
	if mat != nil {
		t.Fatal("a refused Join must return no material")
	}
	assertNoEnrollmentMaterial(t, dir)
}

// TestResolveCAJoinNeverPersistsACAKeyAnOlderMemberServes is the ResolveCA
// counterpart: its join branch used to split the fetched bundle and persist
// the CA key as this node's own.
func TestResolveCAJoinNeverPersistsACAKeyAnOlderMemberServes(t *testing.T) {
	bootstrap := bundleServer(t, legacyKeyBundle(t))
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63031",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bootstrap,
	}

	caCert, caKey, adv, err := ResolveCA(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("ResolveCA must refuse a join: the CA key is not distributed")
	}
	adv.Stop()
	if len(caCert) != 0 || len(caKey) != 0 {
		t.Fatal("a refused ResolveCA join must return no CA material")
	}
	assertNoEnrollmentMaterial(t, dir)
}

// TestResolveJoinPathNeverPersistsACAKeyAnOlderMemberServes covers Resolve
// path 4, the one the daemon reaches with bootstrap flags set.
func TestResolveJoinPathNeverPersistsACAKeyAnOlderMemberServes(t *testing.T) {
	bootstrap := bundleServer(t, legacyKeyBundle(t))
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63032",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   bootstrap,
	}

	mat, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("Resolve path 4 must refuse: the CA key is not distributed")
	}
	adv.Stop()
	if mat != nil {
		t.Fatal("a refused join must return no material")
	}
	assertNoEnrollmentMaterial(t, dir)
}

// TestNoisePayloadNeverCarriesTheCAPrivateKey fetches the bundle over a real
// bootstrap session from a real first node and asserts on what arrives: the
// PEM bundle and every DER certificate in the response. The client pins both
// suites, so the session is Noise NK where this runtime may use it and P-256
// under FIPS 140-3 mode, where the first node holds no Noise key.
func TestNoisePayloadNeverCarriesTheCAPrivateKey(t *testing.T) {
	adv, _ := startFirstNode(t)

	client, err := noiseboot.NewClient(&noiseboot.ClientConfig{
		ServerAddr:          adv.Addr(),
		ServerStaticKey:     adv.NoisePublicKey(),
		ServerP256StaticKey: adv.P256PublicKey(),
		Logger:              discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = client.Close() }()

	resp, err := client.GetCABundle(ctx, "", "")
	if err != nil {
		t.Fatalf("GetCABundle: %v", err)
	}
	assertNoPrivateKeyPEM(t, "the Noise bundle PEM", []byte(resp.BundlePEM))
	if len(resp.Certificates) == 0 {
		t.Fatal("the Noise response carries no certificate at all")
	}
	for i, c := range resp.Certificates {
		assertNoPrivateKeyPEM(t, "Noise certificate "+strconv.Itoa(i), []byte(c))
	}
}

// TestAdvertiserBundleProviderNeverCarriesTheCAPrivateKey asserts on the
// bytes this package hands the Noise server, before go-truststrap's handler
// touches them. The handler happens to rebuild its response from parsed
// certificates today, which is why the wire-level test above passed even
// while this package handed it the key; this package's own guarantee must
// not rest on a property of a dependency's handler.
func TestAdvertiserBundleProviderNeverCarriesTheCAPrivateKey(t *testing.T) {
	var captured noiseboot.BundleProvider
	prev := newNoiseServer
	newNoiseServer = func(cfg *noiseboot.ServerConfig) (*noiseboot.Server, error) {
		captured = cfg.CABundler
		return prev(cfg)
	}
	t.Cleanup(func() { newNoiseServer = prev })

	mat, err := clustertls.GenerateCABundle()
	if err != nil {
		t.Fatalf("GenerateCABundle: %v", err)
	}
	if len(mat.CAKeyPEM) == 0 {
		t.Fatal("fixture: the first node's material must hold the CA key")
	}
	adv, err := StartAdvertiser(mat, AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost},
		t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAdvertiser: %v", err)
	}
	t.Cleanup(adv.Stop)

	if captured == nil {
		t.Fatal("the Noise server was not constructed through the seam")
	}
	bundle, err := captured.CABundle()
	if err != nil {
		t.Fatalf("CABundle: %v", err)
	}
	assertNoPrivateKeyPEM(t, "the advertiser's bundle provider", bundle)
	if !bytes.Equal(bundle, mat.CACertPEM) {
		t.Fatalf("provider serves %q, want exactly the CA certificate", bundle)
	}
}

// TestStartAdvertiserRefusesCertificatelessMaterial: material with no CA
// certificate leaves nothing an advertiser may serve.
func TestStartAdvertiserRefusesCertificatelessMaterial(t *testing.T) {
	_, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	adv, err := StartAdvertiser(&clustertls.Material{CACertPEM: caKeyPEM, CAKeyPEM: caKeyPEM},
		AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err == nil {
		adv.Stop()
		t.Fatal("StartAdvertiser must refuse material whose CA certificate is key material")
	}
	var want *AnchorError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *AnchorError", err, err)
	}
}

// TestJoinIsRefusedWithATypedEnrollmentUnavailableError pins the error an
// operator sees, and that it is raised before any network dial: the
// bootstrap endpoint here does not exist, and a fetch attempt would surface
// as *FetchError instead.
func TestJoinIsRefusedWithATypedEnrollmentUnavailableError(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{
		RaftAddress: "127.0.0.1:63033",
		TrustDomain: "test.cluster",
		DataDir:     dir,
		Bootstrap:   BootstrapOptions{DirectURL: "https://127.0.0.1:1/v1/ca/bootstrap"},
	}

	for name, run := range map[string]func() error{
		"Join": func() error {
			_, err := Join(context.Background(), opts, discardLogger())
			return err
		},
		"ResolveCA": func() error {
			_, _, _, err := ResolveCA(context.Background(), opts, discardLogger())
			return err
		},
		"Resolve": func() error {
			_, _, err := Resolve(context.Background(), opts, discardLogger())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			var want *EnrollmentUnavailableError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *EnrollmentUnavailableError", err, err)
			}
			var fetch *FetchError
			if errors.As(err, &fetch) {
				t.Fatal("the refusal must come before any bootstrap fetch")
			}
			msg := err.Error()
			for _, want := range []string{"not distributed", "--tls-ca-file"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q does not mention %q", msg, want)
				}
			}
			assertNoEnrollmentMaterial(t, dir)
		})
	}
}

// TestFetchAnchorStillRefusesAKeyBundle: the attested-mode join fetches an
// anchor and must keep failing closed on a bundle that carries a key.
func TestFetchAnchorStillRefusesAKeyBundle(t *testing.T) {
	bootstrap := bundleServer(t, legacyKeyBundle(t))
	_, err := FetchAnchor(context.Background(), &Options{DataDir: t.TempDir(), Bootstrap: bootstrap}, discardLogger())
	var want *AnchorError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *AnchorError", err, err)
	}
}
