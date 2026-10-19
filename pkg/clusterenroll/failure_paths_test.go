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
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// startSPKIJoinFixture serves the CA bootstrap and join-over-HTTPS
// endpoints a joining node dials on the SPKI/Direct path, backed by a real
// cluster CA provider, and returns the cluster CA certificate and the
// bootstrap options that join raftAddress through it.
func startSPKIJoinFixture(t *testing.T, raftAddress string) (*x509.Certificate, BootstrapOptions) {
	t.Helper()
	provider := newTestClusterCAProvider(t)
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	caCertPEM, err := clusterca.CACertPEM(context.Background(), provider)
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}

	store := kvstore.NewMemoryKVStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(caCertPEM)
	})
	mux.HandleFunc(JoinHTTPPath, func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		resp := HandleJoinRequest(r.Context(), provider, store, testTrustDomain, &req, nil)
		respBody, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "marshal failed", http.StatusInternalServerError)
			return
		}
		if resp.Error != "" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)

	return caCert, BootstrapOptions{
		SPKIPin:     hex.EncodeToString(sum[:]),
		ServerAddr:  strings.TrimPrefix(srv.URL, "https://"),
		JoinHTTPURL: srv.URL,
		JoinToken:   mintTestJoinToken(t, store, raftAddress),
	}
}

// occupyWithDirectory puts a non-empty directory at path, so an atomic
// write's final rename onto it fails for any user, root included.
func occupyWithDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
}

// fileBlockedDir returns a directory path whose parent is a regular file,
// so every create under it fails with ENOTDIR.
func fileBlockedDir(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	return filepath.Join(blocker, "denied")
}

func TestResolve_JoinPathReturnsTheJoinedMaterial(t *testing.T) {
	const raftAddress = "127.0.0.1:63101"
	caCert, bootstrap := startSPKIJoinFixture(t, raftAddress)
	opts := &Options{
		RaftAddress: raftAddress,
		TrustDomain: testTrustDomain,
		DataDir:     t.TempDir(),
		Bootstrap:   bootstrap,
	}

	mat, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Resolve (join path): %v", err)
	}
	if adv != nil {
		t.Fatal("a joined node must not start an enrollment advertiser")
	}
	block, _ := pem.Decode(mat.CertPEM)
	if block == nil {
		t.Fatal("joined material has no leaf certificate PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("the joined leaf does not chain to the cluster CA: %v", err)
	}
}

func TestJoin_RoleWriteFailureIsTyped(t *testing.T) {
	const raftAddress = "127.0.0.1:63102"
	_, bootstrap := startSPKIJoinFixture(t, raftAddress)
	dir := t.TempDir()
	occupyWithDirectory(t, filepath.Join(dir, RoleFile))
	opts := &Options{RaftAddress: raftAddress, TrustDomain: testTrustDomain, DataDir: dir, Bootstrap: bootstrap}

	_, err := Join(context.Background(), opts, discardLogger())
	var writeErr *clustertls.WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("Join with an unwritable role file = %v, want *clustertls.WriteError", err)
	}
	if writeErr.Path != filepath.Join(dir, RoleFile) {
		t.Errorf("WriteError.Path = %q, want the role file", writeErr.Path)
	}
}

func TestJoin_MaterialWriteFailureIsTyped(t *testing.T) {
	const raftAddress = "127.0.0.1:63103"
	_, bootstrap := startSPKIJoinFixture(t, raftAddress)
	dir := t.TempDir()
	occupyWithDirectory(t, filepath.Join(dir, "node-key.pem"))
	opts := &Options{RaftAddress: raftAddress, TrustDomain: testTrustDomain, DataDir: dir, Bootstrap: bootstrap}

	_, err := Join(context.Background(), opts, discardLogger())
	var writeErr *clustertls.WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("Join with unwritable material = %v, want *clustertls.WriteError", err)
	}
	if writeErr.Path != filepath.Join(dir, "node-key.pem") {
		t.Errorf("WriteError.Path = %q, want the node key file", writeErr.Path)
	}
}

func TestResolve_PartialCachedMaterialIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("ca"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	opts := &Options{RaftAddress: "127.0.0.1:63104", TrustDomain: testTrustDomain, DataDir: dir, SingleNode: true}

	_, _, err := Resolve(context.Background(), opts, discardLogger())
	var partial *clustertls.PartialMaterialError
	if !errors.As(err, &partial) {
		t.Fatalf("Resolve over a partial cache = %v, want *clustertls.PartialMaterialError", err)
	}
}

func TestResolve_FirstNodeRoleWriteFailureIsTyped(t *testing.T) {
	opts := &Options{
		RaftAddress: "127.0.0.1:63105",
		TrustDomain: testTrustDomain,
		DataDir:     fileBlockedDir(t),
		Advertise:   AdvertiseOptions{NoisePort: freePort(t), NoiseHost: testBindHost},
	}

	_, adv, err := Resolve(context.Background(), opts, discardLogger())
	if adv != nil {
		adv.Stop()
		t.Fatal("no advertiser may start when the role cannot be recorded")
	}
	var writeErr *clustertls.WriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("first-node Resolve with an unwritable data dir = %v, want *clustertls.WriteError", err)
	}
}

func TestResolveCA_UnreadableCachedCAIsTyped(t *testing.T) {
	dir := t.TempDir()
	occupyWithDirectory(t, filepath.Join(dir, "ca.pem"))
	if err := os.WriteFile(filepath.Join(dir, "ca-key.pem"), []byte("key"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	opts := &Options{RaftAddress: "127.0.0.1:63106", TrustDomain: testTrustDomain, DataDir: dir, SingleNode: true}

	_, _, _, err := ResolveCA(context.Background(), opts, discardLogger())
	var loadErr *clustertls.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("ResolveCA over an unreadable ca.pem = %v, want *clustertls.LoadError", err)
	}
}

func TestJoinCSRHTTP_InvalidPinIsFetchError(t *testing.T) {
	_, csrDER := generateTestCSR(t)
	_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
		ServerURL: "https://127.0.0.1:1",
		SPKIPin:   "not-hex",
	}, csrDER, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSRHTTP with an invalid pin = %v, want *FetchError", err)
	}
}

// pinnedJoinHTTPServer serves handler at JoinHTTPPath over TLS and returns
// client options pinned to it.
func pinnedJoinHTTPServer(t *testing.T, handler http.HandlerFunc) *JoinHTTPClientOptions {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(JoinHTTPPath, handler)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return &JoinHTTPClientOptions{ServerURL: srv.URL, SPKIPin: hex.EncodeToString(sum[:]), Token: "tok"}
}

func TestJoinCSRHTTP_PendingResponseIsJoinPendingError(t *testing.T) {
	opts := pinnedJoinHTTPServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(JoinResponse{Pending: true, RequestID: "req-1"})
	})
	_, csrDER := generateTestCSR(t)

	_, err := JoinCSRHTTP(context.Background(), opts, csrDER, "")
	var pending *JoinPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("JoinCSRHTTP against a pending response = %v, want *JoinPendingError", err)
	}
	if pending.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", pending.RequestID, "req-1")
	}
}

func TestJoinCSRHTTP_TruncatedBodyIsFetchError(t *testing.T) {
	opts := pinnedJoinHTTPServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// A declared length the body never reaches: the client's read ends
		// in an unexpected EOF rather than a short, parseable body.
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte("{"))
	})
	_, csrDER := generateTestCSR(t)

	_, err := JoinCSRHTTP(context.Background(), opts, csrDER, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSRHTTP against a truncated body = %v, want *FetchError", err)
	}
}

func TestMintJoinToken_NegativeTTLRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, _, err := MintJoinToken(context.Background(), store, testTrustDomain, "node-1", -time.Second)
	var ttlErr *JoinTokenTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("MintJoinToken with a negative ttl = %v, want *JoinTokenTTLError", err)
	}
	if ttlErr.TTL != -time.Second {
		t.Errorf("TTL = %v, want the negative value as sent", ttlErr.TTL)
	}
}

func TestConsumeJoinToken_RecordWithoutANodeIdentityRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		nodeURI string
	}{
		{name: "unparseable URI", nodeURI: "://no-scheme"},
		{name: "tenant identity", nodeURI: "spiffe://test.cluster/tenant/acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := kvstore.NewMemoryKVStore()
			token := "token-" + strings.ReplaceAll(tc.name, " ", "-")
			data, err := json.Marshal(joinTokenRecord{NodeURI: tc.nodeURI, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if err := store.Put(context.Background(), joinTokenPrefix+invite.TokenDigest(token), data); err != nil {
				t.Fatalf("Put: %v", err)
			}

			_, err = ConsumeJoinToken(context.Background(), store, testTrustDomain, token)
			assertJoinRefused(t, err)
		})
	}
}

func TestMarkJoinIdentityAdmitted_AlreadyRecordedIsNotAnError(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const nodeURI = "spiffe://test.cluster/node/node-1"
	notAfter := time.Now().Add(time.Hour)
	if err := MarkJoinIdentityAdmitted(context.Background(), store, nodeURI, notAfter, "01"); err != nil {
		t.Fatalf("first MarkJoinIdentityAdmitted: %v", err)
	}
	first, err := store.Get(context.Background(), joinIdentityPrefix+nodeURI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := MarkJoinIdentityAdmitted(context.Background(), store, nodeURI, notAfter.Add(time.Hour), "02"); err != nil {
		t.Fatalf("second MarkJoinIdentityAdmitted = %v, want nil", err)
	}
	second, err := store.Get(context.Background(), joinIdentityPrefix+nodeURI)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(first) != string(second) {
		t.Error("a second admission overwrote the recorded one")
	}
}

func TestLoadOrGenerateP256Key_WriteFailure(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), P256KeyFile)
	occupyWithDirectory(t, keyPath)

	key, err := loadOrGenerateP256Key(keyPath, "noise", "bootstrap", discardLogger())
	if err == nil {
		key.Destroy()
		t.Fatal("expected a failure writing the key file")
	}
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("error = %v, want *ServerError", err)
	}
}

func TestLoadOrGenerateJoinKey_ReadErrorIsWrapped(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), JoinKeyFile)
	if err := os.WriteFile(keyPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	orig := readFileFn
	t.Cleanup(func() { readFileFn = orig })
	wantErr := errors.New("read refused")
	readFileFn = func(string) ([]byte, error) { return nil, wantErr }

	key, err := loadOrGenerateJoinKey(keyPath, discardLogger())
	if err == nil {
		key.Destroy()
		t.Fatal("expected a read failure")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want it to wrap the read failure", err)
	}
}

func TestStartJoinServer_KeyFailuresAreServerErrors(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	opts := JoinServerOptions{Host: testBindHost, Store: kvstore.NewMemoryKVStore(), TrustDomain: testTrustDomain}

	t.Run("unwritable data dir", func(t *testing.T) {
		js, err := StartJoinServer(provider, opts, fileBlockedDir(t), discardLogger())
		if err == nil {
			js.Stop()
			t.Fatal("expected a failure creating the join keys")
		}
		var serverErr *ServerError
		if !errors.As(err, &serverErr) {
			t.Fatalf("error = %v, want *ServerError", err)
		}
	})

	t.Run("corrupt P-256 key", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, JoinP256KeyFile), []byte("short"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		js, err := StartJoinServer(provider, opts, dir, discardLogger())
		if err == nil {
			js.Stop()
			t.Fatal("expected a corrupt P-256 key to be refused")
		}
		var sizeErr *P256KeyFileSizeError
		if !errors.As(err, &sizeErr) {
			t.Fatalf("error = %v, want *P256KeyFileSizeError", err)
		}
	})
}

func TestJoinServer_P256PublicKey_NilAndZeroValue(t *testing.T) {
	var nilServer *JoinServer
	if got := nilServer.P256PublicKey(); got != nil {
		t.Errorf("nil JoinServer P256PublicKey = %x, want nil", got)
	}
	if got := (&JoinServer{}).P256PublicKey(); got != nil {
		t.Errorf("zero JoinServer P256PublicKey = %x, want nil", got)
	}
}
