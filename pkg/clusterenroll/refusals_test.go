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
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func TestLeafExpiryParseError_CarriesAndUnwrapsTheCause(t *testing.T) {
	cause := errors.New("truncated DER")
	err := &LeafExpiryParseError{Err: cause}
	if !strings.Contains(err.Error(), "truncated DER") {
		t.Errorf("Error() = %q; want the cause", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is does not reach the cause through Unwrap")
	}
}

func TestLoadOrGenerateJoinKey_RefusesACorruptKeyFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), JoinKeyFile)
	if err := os.WriteFile(keyPath, []byte("zz-not-hex"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	key, err := loadOrGenerateJoinKey(keyPath, discardLogger())
	if key != nil {
		key.Destroy()
		t.Fatal("a corrupt key file produced a key")
	}
	var serr *ServerError
	if !errors.As(err, &serr) || serr.Protocol != "join" {
		t.Fatalf("err = %v; want a join *ServerError", err)
	}
	// The corrupt file is left for the operator, never silently replaced
	// with a new identity.
	data, rerr := os.ReadFile(keyPath)
	if rerr != nil || string(data) != "zz-not-hex" {
		t.Errorf("key file after refusal = %q, %v; want it untouched", data, rerr)
	}
}

func TestJoinApprovalPollInterval_DefaultsWhenUnset(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if got := (BootstrapOptions{JoinApprovalPollInterval: d}).joinApprovalPollInterval(); got != DefaultJoinApprovalPollInterval {
			t.Errorf("interval for %v = %v; want DefaultJoinApprovalPollInterval", d, got)
		}
	}
	if got := (BootstrapOptions{JoinApprovalPollInterval: 3 * time.Second}).joinApprovalPollInterval(); got != 3*time.Second {
		t.Errorf("configured interval = %v; want 3s", got)
	}
}

// issueAndAdmitRequest builds the inputs issueAndAdmit takes for one identity.
func issueAndAdmitRequest(t *testing.T, nodeID string) (string, *x509.CertificateRequest) {
	t.Helper()
	nodeURI := spiffe.NodeURI(testTrustDomain, nodeID)
	_, csrDER := buildMatchingJoinCSR(t, nodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	return nodeURI, csr
}

func TestIssueAndAdmit_PromoteFailureWithholdsTheSignedLeaf(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failCompareAndSwapOnPrefix: joinIdentityPrefix}
	nodeURI, csr := issueAndAdmitRequest(t, "node-promote-fault")
	adv := &fakeFeatureAdvertiser{}

	resp := issueAndAdmit(context.Background(), newTestClusterCAProvider(t), store, nodeURI, csr, 7, []string{"f"}, adv)
	if resp.Error != "join request could not be processed" || len(resp.CertPEM) != 0 {
		t.Fatalf("response = %+v; want the generic refusal and no certificate", resp)
	}
	if n := adv.callCount(); n != 0 {
		t.Errorf("advertiser called %d times for a refused join; want 0", n)
	}
}

func TestLoadRole_UnreadableRoleFileIsAnErrorNotAMissingRole(t *testing.T) {
	dir := t.TempDir()
	// A directory where the role file belongs: the read fails with an
	// error other than "does not exist".
	if err := os.Mkdir(filepath.Join(dir, RoleFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	role, found, err := loadRole(dir)
	if err == nil {
		t.Fatal("an unreadable role file loaded without error")
	}
	if found || role != "" {
		t.Errorf("loadRole = (%q, %v); want no role reported", role, found)
	}

	adv, err := ResumeAdvertiser(firstNodeOpts(t, dir, freePort(t)), &clustertls.Material{CACertPEM: []byte("x")}, discardLogger())
	if err == nil || adv != nil {
		t.Fatalf("ResumeAdvertiser = (%v, %v); want the read error", adv, err)
	}
	var mre *MissingRoleError
	if errors.As(err, &mre) {
		t.Error("an unreadable role file was reported as a missing one")
	}
}

// firstNodeDir returns a data directory whose recorded role is first-node.
func firstNodeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := persistRole(dir, RoleFirstNode); err != nil {
		t.Fatalf("persistRole: %v", err)
	}
	return dir
}

func TestResumeAdvertiser_FirstNodeWithoutCACertificateRefuses(t *testing.T) {
	dir := firstNodeDir(t)
	adv, err := ResumeAdvertiser(firstNodeOpts(t, dir, freePort(t)), &clustertls.Material{}, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("a first node with no CA certificate started an advertiser")
	}
	var ierr *IncompleteFirstNodeMaterialError
	if !errors.As(err, &ierr) {
		t.Fatalf("err = %v; want *IncompleteFirstNodeMaterialError", err)
	}
	if ierr.Dir != dir || len(ierr.Missing) != 1 || ierr.Missing[0] != "ca.pem" {
		t.Errorf("error = %+v; want it to name ca.pem in %s", ierr, dir)
	}
}

func TestResumeAdvertiser_FirstNodeWithoutBindHostRefuses(t *testing.T) {
	dir := firstNodeDir(t)
	opts := firstNodeOpts(t, dir, freePort(t))
	opts.Advertise.NoiseHost = ""
	adv, err := ResumeAdvertiser(opts, &clustertls.Material{CACertPEM: []byte("x")}, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("a first node with no bind host started an advertiser")
	}
	var berr *MissingBindHostError
	if !errors.As(err, &berr) {
		t.Fatalf("err = %v; want *MissingBindHostError", err)
	}
}

func TestResumeAdvertiser_RefusesToServeABundleHoldingAPrivateKey(t *testing.T) {
	dir := firstNodeDir(t)
	bundle := append([]byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: make([]byte, 3)})...)
	adv, err := ResumeAdvertiser(firstNodeOpts(t, dir, freePort(t)), &clustertls.Material{CACertPEM: bundle}, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("an advertiser started serving a bundle that holds private-key material")
	}
	var aerr *AnchorError
	if !errors.As(err, &aerr) {
		t.Fatalf("err = %v; want *AnchorError", err)
	}
	if !strings.Contains(aerr.Reason, "private-key") {
		t.Errorf("AnchorError.Reason = %q; want it to name the private key", aerr.Reason)
	}
}
