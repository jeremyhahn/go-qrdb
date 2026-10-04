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

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

const cliFIDO = "fido2"

// cliHolderStack is a real Shamir barrier behind a real ceremony service,
// wired the way a composer wires them: the CLI's holder verbs run against it
// through the embedded transport.
type cliHolderStack struct {
	svc service.BarrierServicer
	// barrier backs svc (ceremony-backed) and is kept so a caller that
	// needs to initialize Shamir custody can do so through a plain,
	// non-ceremony service instead: svc's ceremony now refuses
	// BarrierInitializeShamir (go-qrdb#560 review item 8), the same way
	// it already refused BarrierInitialize.
	barrier *seal.StorageBarrier
}

func newCLIHolderStack(t *testing.T, threshold, total int) *cliHolderStack {
	t.Helper()
	ca, _ := ceremonytest.NewCA(t, "CLI Holder CA")
	shamirStrategy, err := seal.NewShamirStrategy(threshold, total)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	barrier, err := seal.NewStorageBarrier(log, seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &seal.ShamirConfig{Threshold: threshold, TotalShares: total},
	}, shamirStrategy)
	if err != nil {
		t.Fatal(err)
	}
	factors := authfactors.NewFactorRegistry()
	if err := factors.Register(factortest.New(cliFIDO, time.Minute)); err != nil {
		t.Fatal(err)
	}
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, log)
	if err != nil {
		t.Fatal(err)
	}
	core.SetCA(ca)
	core.SetFactorRegistry(factors)
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", cliSOPIN); err != nil {
		t.Fatal(err)
	}
	core.SetPINBackend(pins)
	return &cliHolderStack{svc: service.NewBarrierServiceWithCeremony(barrier, core), barrier: barrier}
}

// cliSOPIN is the barrier SO PIN the CLI holder stack provisions; rekey and
// revocation of the system barrier present it.
const cliSOPIN = "so-pin-1234"

// cliHolder is one holder's files: its certificate key, CSR, the certificate
// it is issued, an optional separate X25519 envelope key and an optional
// assertion authenticator.
type cliHolder struct {
	name, role string
	dir        string
	keyFile    string
	csrFile    string
	envFile    string
	encPubFile string
	encPrvFile string
	fido       *factortest.Authenticator
	cert       *x509.Certificate
}

func newCLIHolder(t *testing.T, name, role string, x25519, fido bool) *cliHolder {
	t.Helper()
	h := &cliHolder{name: name, role: role, dir: t.TempDir()}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	h.keyFile = h.write(t, "key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	h.csrFile = h.write(t, "csr.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))
	h.envFile = filepath.Join(h.dir, "envelope.json")
	if x25519 {
		suite, err := keydelivery.LookupSuite(keydelivery.KEMX25519)
		if err != nil {
			t.Fatal(err)
		}
		sk, err := suite.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		priv, err := sk.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		h.encPubFile = h.write(t, "enc.pub", sk.PublicKey().Bytes())
		h.encPrvFile = h.write(t, "enc.key", priv)
	}
	if fido {
		if h.fido, err = factortest.NewAuthenticator(); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *cliHolder) write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(h.dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ctx is a context carrying the holder's certificate as the verified TLS
// peer, as a transport would bind it.
func (h *cliHolder) ctx() context.Context {
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{h.cert}})
}

// registerArgs are the holder's enrolment flags.
func (h *cliHolder) registerArgs() []string {
	args := []string{"--username", h.name, "--role", h.role, "--csr-file", h.csrFile}
	if h.encPubFile != "" {
		args = append(args, "--encryption-kem", keydelivery.KEMX25519, "--encryption-key-file", h.encPubFile)
	}
	if h.fido != nil {
		params, _ := json.Marshal(h.fido.EnrollParams())
		args = append(args, "--factor", cliFIDO+"="+string(params), "--designated", cliFIDO)
	}
	return args
}

// submitArgs submit the holder's share to op, answering mtls with --key.
func (h *cliHolder) submitArgs(op string) []string {
	args := []string{"--op", op, "--username", h.name, "--envelope", h.envFile, "--key", h.keyFile}
	if h.encPrvFile != "" {
		args = append(args, "--encryption-key-file", h.encPrvFile)
	}
	return args
}

// runCLI executes the root command with ctx, capturing its output.
func runCLI(root *cobra.Command, ctx context.Context, args ...string) (string, error) {
	var buf bytes.Buffer
	savedWriter, savedFormat, savedClient := outputWriter, outputFormat, client
	outputWriter = &buf
	defer func() { outputWriter, outputFormat, client = savedWriter, savedFormat, savedClient }()
	root.SetArgs(args)
	root.SetOut(&buf)
	root.SetErr(&buf)
	err := root.ExecuteContext(ctx)
	return buf.String(), err
}

// mustCLI runs the CLI and fails on an error.
func mustCLI(t *testing.T, ctx context.Context, root func() *cobra.Command, args ...string) string {
	t.Helper()
	out, err := runCLI(root(), ctx, args...)
	if err != nil {
		t.Fatalf("qrdb %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// submitViaFIDO runs the external-factor path: quorum begin, answer with the
// authenticator, then quorum submit with the challenge id and response file.
func submitViaFIDO(t *testing.T, root func() *cobra.Command, h *cliHolder, op string) string {
	t.Helper()
	out := mustCLI(t, context.Background(), root, "--output", "json", "barrier", "quorum", "begin", "--op", op, "--username", h.name)
	var ch transport.QuorumChallenge
	if err := json.Unmarshal([]byte(out), &ch); err != nil {
		t.Fatalf("challenge %q: %v", out, err)
	}
	if ch.Factor != cliFIDO {
		t.Fatalf("challenge factor = %q, want the designated %q", ch.Factor, cliFIDO)
	}
	resp := h.write(t, "response.bin", h.fido.Assert(ch.Nonce))
	args := []string{"barrier", "quorum", "submit", "--op", op, "--username", h.name, "--envelope", h.envFile,
		"--challenge-id", ch.ChallengeID, "--factor", cliFIDO, "--response-file", resp,
		"--response-metadata", `{"client":"cli-test"}`}
	if h.encPrvFile != "" {
		args = append(args, "--encryption-key-file", h.encPrvFile)
	}
	return mustCLI(t, context.Background(), root, args...)
}

// claimViaSplitHandshake runs claim-begin, signs outside the CLI, and
// completes with claim-complete.
func claimViaSplitHandshake(t *testing.T, root func() *cobra.Command, h *cliHolder) {
	t.Helper()
	out := mustCLI(t, h.ctx(), root, "--output", "json", "barrier", "officer", "claim-begin", "--username", h.name)
	var begin transport.ShareClaimBeginResponse
	if err := json.Unmarshal([]byte(out), &begin); err != nil {
		t.Fatal(err)
	}
	signer, err := readSigner(h.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	// The external signer signs exactly the message claim-begin printed.
	var printed claimBeginOutput
	if err := json.Unmarshal([]byte(out), &printed); err != nil {
		t.Fatal(err)
	}
	msg, err := hex.DecodeString(printed.SignMessageHex)
	if err != nil {
		t.Fatal(err)
	}
	if want := ceremony.ShareClaimMessage(ceremony.ScopeSystem.ClaimLabel(), h.name, mustHexNonce(t, begin.NonceHex)); !bytes.Equal(msg, want) {
		t.Fatalf("claim-begin printed message %x, want %x", msg, want)
	}
	sig, err := mtls.SignNonce(signer, msg)
	if err != nil {
		t.Fatal(err)
	}
	sigFile := h.write(t, "sig.bin", sig)
	mustCLI(t, h.ctx(), root, "barrier", "officer", "claim-complete", "--username", h.name,
		"--nonce-hex", begin.NonceHex, "--sig-file", sigFile, "--out", h.envFile)
}

// TestHolderCLI_Lifecycle drives every system holder verb against a real
// barrier and ceremony: enrolment of officers and custodians, Shamir
// initialization, both claim flows, root-token and rekey quorums answered by
// the mtls factor and by an external assertion factor, and revocation.
func TestHolderCLI_Lifecycle(t *testing.T) {
	stack := newCLIHolderStack(t, 2, 3)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
			embedded.WithBarrierService(stack.svc), embedded.WithPINBackend(pintest.NewBackend(t)))
	}
	alice := newCLIHolder(t, "alice", transport.RoleSecurityOfficer, false, false)
	carol := newCLIHolder(t, "carol", transport.RoleCustodian, true, true)
	erin := newCLIHolder(t, "erin", transport.RoleCustodian, false, false)
	holders := []*cliHolder{alice, carol, erin}

	for _, h := range holders {
		out := mustCLI(t, context.Background(), root, append([]string{"--output", "json", "barrier", "officer", "register"}, h.registerArgs()...)...)
		var resp transport.OfficerRegisterResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("register %s: %q: %v", h.name, out, err)
		}
		cert, err := mtls.ParseCertificatePEM([]byte(resp.CertPEM))
		if err != nil || cert.Subject.CommonName != h.name {
			t.Fatalf("%s certificate: %v (CN %q)", h.name, err, cert.Subject.CommonName)
		}
		h.cert = cert
	}
	if out := mustCLI(t, context.Background(), root, "barrier", "officer", "register", "--username", "table-only", "--csr-file", newCLIHolder(t, "x", "so", false, false).csrFile); !strings.Contains(out, "CertPEM:") {
		t.Fatalf("register table output = %q", out)
	}
	// A plain, non-ceremony BarrierService over the same barrier, wired
	// into its OWN command tree: stack.svc's ceremony now refuses
	// BarrierInitializeShamir (go-qrdb#560 review item 8), the same way
	// it already refused BarrierInitialize -- an operator's `barrier
	// init-shamir` against a real ceremony-backed system barrier must be
	// refused and pointed at `qrdb init` instead. This test is driving
	// the holder-custody LIFECYCLE (enrolment, claim, quorum, rekey,
	// revocation), not that bootstrap gate, so it initializes custody
	// through the plain path precisely as the fixed BarrierService
	// requires; `root` (stack.svc, ceremony-backed) is unaffected and
	// still used for every verb below.
	plainRoot := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
			embedded.WithBarrierService(service.NewBarrierService(stack.barrier)), embedded.WithPINBackend(pintest.NewBackend(t)))
	}
	mustCLI(t, context.Background(), plainRoot, "barrier", "init-shamir", "--threshold", "2", "--total", "4")

	claimAll := func() {
		t.Helper()
		mustCLI(t, alice.ctx(), root, "barrier", "officer", "claim", "--username", alice.name, "--key", alice.keyFile, "--out", alice.envFile)
		mustCLI(t, carol.ctx(), root, "barrier", "officer", "claim", "--username", carol.name, "--key", carol.keyFile, "--out", carol.envFile)
		claimViaSplitHandshake(t, root, erin)
	}
	claimAll()
	if out := mustCLI(t, context.Background(), root, "barrier", "officer", "list"); !strings.Contains(out, "carol") || !strings.Contains(out, "role=custodian") {
		t.Fatalf("list = %q", out)
	}

	// Root token: alice answers mtls, carol her FIDO2 assertion.
	mustCLI(t, context.Background(), root, append([]string{"barrier", "quorum", "submit"}, alice.submitArgs(transport.OpRootToken)...)...)
	if out := submitViaFIDO(t, root, carol, transport.OpRootToken); !strings.Contains(out, "RootToken:") {
		t.Fatalf("root-token completion = %q", out)
	}

	// A rekey needs a fresh quorum; the new split must be claimed again.
	if out := mustCLI(t, context.Background(), root, "barrier", "rekey", "--threshold", "2", "--so-pin", cliSOPIN); !strings.Contains(out, "Round:") {
		t.Fatalf("rekey start = %q", out)
	}
	mustCLI(t, context.Background(), root, append([]string{"barrier", "quorum", "submit"}, alice.submitArgs(transport.OpRekey)...)...)
	if out := mustCLI(t, context.Background(), root, append([]string{"--output", "yaml", "barrier", "quorum", "submit"}, erin.submitArgs(transport.OpRekey)...)...); !strings.Contains(out, "complete: true") {
		t.Fatalf("rekey completion = %q", out)
	}
	claimAll()

	// Revoking erin is a quorum-gated rekey of the remaining holders.
	if out := mustCLI(t, context.Background(), root, "barrier", "officer", "revoke", "--username", "erin", "--threshold", "2", "--so-pin", cliSOPIN); !strings.Contains(out, "Removing:   erin") {
		t.Fatalf("revoke = %q", out)
	}
	mustCLI(t, context.Background(), root, append([]string{"barrier", "quorum", "submit"}, alice.submitArgs(transport.OpRekey)...)...)
	if out := submitViaFIDO(t, root, carol, transport.OpRekey); !strings.Contains(out, "NewSplit:") {
		t.Fatalf("revocation completion = %q", out)
	}
	if out := mustCLI(t, context.Background(), root, "barrier", "officer", "list"); strings.Contains(out, "erin") {
		t.Fatalf("erin is still enrolled: %q", out)
	}
}

// TestHolderCLI_Refusals covers the verbs' local validation and file errors.
func TestHolderCLI_Refusals(t *testing.T) {
	stack := newCLIHolderStack(t, 2, 3)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithBarrierService(stack.svc))
	}
	h := newCLIHolder(t, "alice", transport.RoleSecurityOfficer, false, false)
	missing := filepath.Join(t.TempDir(), "missing")

	var invalid *InvalidArgError
	var fileErr *HolderFileError
	var missingArg *MissingArgError
	for name, tc := range map[string]struct {
		args   []string
		target any
	}{
		"bad domain":        {[]string{"barrier", "officer", "list", "--domain", "bogus"}, &invalid},
		"recovery listing":  {[]string{"barrier", "officer", "list", "--domain", "recovery"}, &invalid},
		"missing csr":       {[]string{"barrier", "officer", "register", "--username", "u", "--csr-file", missing}, &fileErr},
		"missing enc key":   {[]string{"barrier", "officer", "register", "--username", "u", "--csr-file", h.csrFile, "--encryption-key-file", missing}, &fileErr},
		"bad factor params": {[]string{"barrier", "officer", "register", "--username", "u", "--csr-file", h.csrFile, "--factor", "fido2={"}, &invalid},
		"missing key":       {[]string{"barrier", "officer", "claim", "--username", "u", "--key", missing, "--out", missing}, &fileErr},
		"missing sig":       {[]string{"barrier", "officer", "claim-complete", "--username", "u", "--nonce-hex", "ab", "--sig-file", missing, "--out", missing}, &fileErr},
		"missing envelope":  {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", missing}, &fileErr},
		"no factor answer":  {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", h.csrFile}, &missingArg},
		"challenge without response": {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", h.csrFile,
			"--challenge-id", "c1"}, &missingArg},
		"bad metadata": {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", h.csrFile,
			"--response-metadata", "[1]"}, &invalid},
		"missing response file": {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", h.csrFile,
			"--response-file", missing}, &fileErr},
		"missing submit key": {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", h.csrFile,
			"--key", missing}, &fileErr},
		"missing submit enc key": {[]string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", h.csrFile,
			"--encryption-key-file", missing}, &fileErr},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runCLI(root(), context.Background(), tc.args...)
			if err == nil || !errors.As(err, tc.target) {
				t.Fatalf("err = %v (%T)", err, err)
			}
		})
	}

	// A claim over a connection that presents no certificate is refused.
	if _, err := runCLI(root(), context.Background(), "barrier", "officer", "claim-begin", "--username", "alice"); err == nil {
		t.Fatal("a claim without a client certificate was answered")
	}
	// A key that signs is required to answer; a malformed envelope is refused.
	if _, err := openEnvelope([]byte("not an envelope"), nil, nil); err == nil {
		t.Fatal("a malformed envelope opened")
	}
	if _, err := signClaim(nil, systemHolders{domain: transport.DomainUnseal}, "alice", "zz"); err == nil {
		t.Fatal("a non-hex nonce was signed")
	}
	if _, err := claimMessageHex(systemHolders{domain: transport.DomainUnseal}, "alice", "zz"); err == nil {
		t.Fatal("a non-hex nonce produced a claim message")
	}
	if (&HolderFileError{Path: "p", Err: errors.New("x")}).Error() == "" {
		t.Fatal("HolderFileError must describe itself")
	}
}

// TestHolderCLI_TenantVerbs drives the tenant-scoped verbs through the tenant
// service, with a real envelope for the submission.
func TestHolderCLI_TenantVerbs(t *testing.T) {
	var submitted *seal.HolderSubmission
	svc := &mockTenantService{
		officerRegisterFn: func(_ context.Context, id string, r *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
			if id != "acme" || r.Username != "carol" {
				t.Fatalf("register %s %+v", id, r)
			}
			return &ceremony.RegisterOfficerResult{CertPEM: []byte("cert"), Role: r.Role}, nil
		},
		officerListFn: func(context.Context, string) ([]*ceremony.OfficerStatus, error) {
			return []*ceremony.OfficerStatus{{Username: "carol", ClaimedAt: time.Now()}}, nil
		},
		shareClaimBeginFn: func(context.Context, string, string, string) (*ceremony.ChallengeResponse, error) {
			return &ceremony.ChallengeResponse{Nonce: []byte{0xab}}, nil
		},
		shareClaimCompleteFn: func(context.Context, string, string, string, []byte, string) (*ceremony.ShareClaimResult, error) {
			return &ceremony.ShareClaimResult{ShareIndex: 1, Envelope: []byte("sealed")}, nil
		},
		quorumBeginFn: func(_ context.Context, _ string, op seal.QuorumOp, u, f string) (*ceremony.QuorumChallenge, error) {
			return &ceremony.QuorumChallenge{ChallengeID: "c1", Op: op, Holder: u, Factor: "mtls", Nonce: []byte{1, 2, 3}}, nil
		},
		quorumSubmitFn: func(_ context.Context, _ string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
			cp := *sub
			cp.Share = append([]byte(nil), sub.Share...)
			submitted = &cp
			return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
		},
		rekeyStartFn: func(context.Context, string, int, seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
			return &seal.QuorumRoundInfo{Op: seal.OpRekey, Required: 2, NewThreshold: 2}, nil
		},
		officerRevokeFn: func(_ context.Context, _, u string, _ int) (*seal.QuorumRoundInfo, error) {
			return &seal.QuorumRoundInfo{Op: seal.OpRekey, Required: 2, Remove: []string{u}}, nil
		},
	}
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithTenantService(svc))
	}
	carol := newCLIHolder(t, "carol", transport.RoleCustodian, false, false)
	carol.cert = selfSignedCert(t)

	mustCLI(t, context.Background(), root, append([]string{"tenant", "barrier-officer", "register", "--tenant-id", "acme"}, carol.registerArgs()...)...)
	if out := mustCLI(t, context.Background(), root, "tenant", "barrier-officer", "list", "--tenant-id", "acme"); !strings.Contains(out, "carol") {
		t.Fatalf("list = %q", out)
	}
	mustCLI(t, carol.ctx(), root, "tenant", "barrier-officer", "claim", "--tenant-id", "acme", "--username", "carol", "--key", carol.keyFile, "--out", carol.envFile)
	if env, err := os.ReadFile(carol.envFile); err != nil || string(env) != "sealed" {
		t.Fatalf("claimed envelope = %q, %v", env, err)
	}
	mustCLI(t, carol.ctx(), root, "tenant", "barrier-officer", "claim-begin", "--tenant-id", "acme", "--username", "carol")
	sig := carol.write(t, "sig", []byte("sig"))
	mustCLI(t, carol.ctx(), root, "tenant", "barrier-officer", "claim-complete", "--tenant-id", "acme", "--username", "carol",
		"--nonce-hex", hex.EncodeToString([]byte{0xab}), "--sig-file", sig, "--out", carol.envFile)
	mustCLI(t, context.Background(), root, "tenant", "barrier-quorum", "begin", "--tenant-id", "acme", "--op", "unseal", "--username", "carol")
	if out := mustCLI(t, context.Background(), root, "tenant", "barrier-rekey", "--tenant-id", "acme", "--threshold", "2"); !strings.Contains(out, "NewThreshold: 2") {
		t.Fatalf("rekey = %q", out)
	}
	if out := mustCLI(t, context.Background(), root, "tenant", "barrier-officer", "revoke", "--tenant-id", "acme", "--username", "erin"); !strings.Contains(out, "erin") {
		t.Fatalf("revoke = %q", out)
	}
	// The submission opens a real envelope: seal one to carol's key.
	envelope := realEnvelopeFor(t, carol)
	if err := os.WriteFile(carol.envFile, envelope, 0o600); err != nil {
		t.Fatal(err)
	}
	mustCLI(t, context.Background(), root, append([]string{"tenant", "barrier-quorum", "submit", "--tenant-id", "acme"}, carol.submitArgs(transport.OpUnseal)...)...)
	if submitted == nil || submitted.Holder != "carol" || len(submitted.Share) == 0 || submitted.Proof.Factor != mtls.Name {
		t.Fatalf("submission = %+v", submitted)
	}
}

// selfSignedCert returns a throwaway certificate for a TLS peer.
func selfSignedCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "peer"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// realEnvelopeFor enrols h on a real stack, initializes custody, and returns
// the envelope h claims there: a genuine envelope sealed to h's key.
func realEnvelopeFor(t *testing.T, h *cliHolder) []byte {
	t.Helper()
	stack := newCLIHolderStack(t, 2, 2)
	ctx := context.Background()
	other := newCLIHolder(t, "other", transport.RoleSecurityOfficer, false, false)
	var cert *x509.Certificate
	for _, holder := range []*cliHolder{h, other} {
		csr, err := os.ReadFile(holder.csrFile)
		if err != nil {
			t.Fatal(err)
		}
		res, err := stack.svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, &service.BarrierOfficerRegisterRequest{Username: holder.name, CSRPEM: csr})
		if err != nil {
			t.Fatal(err)
		}
		if holder == h {
			if cert, err = mtls.ParseCertificatePEM(res.CertPEM); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := service.NewBarrierService(stack.barrier).BarrierInitializeShamir(ctx, 2, 2, ""); err != nil {
		t.Fatal(err)
	}
	return serviceClaim(t, stack.svc, h, cert)
}

// serviceClaim claims h's envelope straight from svc, presenting cert as the
// connection's certificate, and returns the envelope.
func serviceClaim(t *testing.T, svc service.BarrierServicer, h *cliHolder, cert *x509.Certificate) []byte {
	t.Helper()
	ctx := context.Background()
	thumb := mtls.Thumbprint(cert.Raw)
	begin, err := svc.BarrierShareClaimBegin(ctx, seal.DomainUnseal, h.name, thumb)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := readSigner(h.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signClaim(signer, systemHolders{domain: transport.DomainUnseal}, h.name, begin.NonceHex)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.BarrierShareClaimComplete(ctx, seal.DomainUnseal, h.name, begin.NonceHex, sig, thumb)
	if err != nil {
		t.Fatal(err)
	}
	return res.Envelope
}

// mustHexNonce decodes a hex claim nonce.
func mustHexNonce(t *testing.T, nonceHex string) []byte {
	t.Helper()
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		t.Fatal(err)
	}
	return nonce
}

// TestHolderCLI_EveryVerbRefusesABadDomain: each system holder verb resolves
// its target from --domain first, so an unknown domain is refused locally by
// every verb, before any file is read or any call is made.
func TestHolderCLI_EveryVerbRefusesABadDomain(t *testing.T) {
	stack := newCLIHolderStack(t, 2, 3)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithBarrierService(stack.svc))
	}
	for _, verb := range [][]string{
		{"officer", "register", "--username", "u", "--csr-file", "x"},
		{"officer", "claim", "--username", "u", "--key", "x", "--out", "x"},
		{"officer", "claim-begin", "--username", "u"},
		{"officer", "claim-complete", "--username", "u", "--nonce-hex", "ab", "--sig-file", "x", "--out", "x"},
		{"officer", "revoke", "--username", "u", "--threshold", "2"},
		{"rekey", "--threshold", "2"},
		{"quorum", "begin", "--op", "unseal", "--username", "u"},
		{"quorum", "submit", "--op", "unseal", "--username", "u", "--envelope", "x"},
	} {
		args := append(append([]string{"barrier"}, verb...), "--domain", "bogus")
		var invalid *InvalidArgError
		if _, err := runCLI(root(), context.Background(), args...); !errors.As(err, &invalid) || invalid.Arg != "--domain" {
			t.Errorf("qrdb %s = %v, want an InvalidArgError on --domain", strings.Join(args, " "), err)
		}
	}
}

// TestHolderCLI_ServerRefusalsReachTheCaller: what the barrier refuses --
// a duplicate enrolment, a claim by a name never enrolled, a quorum, rekey or
// revocation before custody is initialized, a submission whose challenge
// cannot be issued and an envelope that does not parse -- comes back from
// every verb as an error, never as output.
func TestHolderCLI_ServerRefusalsReachTheCaller(t *testing.T) {
	stack := newCLIHolderStack(t, 2, 3)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(),
			embedded.WithBarrierService(stack.svc), embedded.WithPINBackend(pintest.NewBackend(t)))
	}
	alice := newCLIHolder(t, "alice", transport.RoleSecurityOfficer, false, false)
	out := mustCLI(t, context.Background(), root, append([]string{"--output", "json", "barrier", "officer", "register"}, alice.registerArgs()...)...)
	var resp transport.OfficerRegisterResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if alice.cert, _ = mtls.ParseCertificatePEM([]byte(resp.CertPEM)); alice.cert == nil {
		t.Fatal("alice's certificate did not parse")
	}
	sig := alice.write(t, "sig.bin", []byte("sig"))
	response := alice.write(t, "response.bin", []byte("response"))

	for name, tc := range map[string]struct {
		ctx  context.Context
		args []string
	}{
		"duplicate enrolment":   {context.Background(), append([]string{"barrier", "officer", "register"}, alice.registerArgs()...)},
		"claim of a stranger":   {alice.ctx(), []string{"barrier", "officer", "claim", "--username", "mallory", "--key", alice.keyFile, "--out", alice.envFile}},
		"begin by a stranger":   {alice.ctx(), []string{"barrier", "officer", "claim-begin", "--username", "mallory"}},
		"complete by stranger":  {alice.ctx(), []string{"barrier", "officer", "claim-complete", "--username", "mallory", "--nonce-hex", "ab", "--sig-file", sig, "--out", alice.envFile}},
		"quorum before custody": {context.Background(), []string{"barrier", "quorum", "begin", "--op", "unseal", "--username", "alice"}},
		"rekey before custody":  {context.Background(), []string{"barrier", "rekey", "--threshold", "2", "--so-pin", cliSOPIN}},
		"revoke before custody": {context.Background(), []string{"barrier", "officer", "revoke", "--username", "alice", "--threshold", "2", "--so-pin", cliSOPIN}},
		"submit, no challenge":  {context.Background(), []string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "alice", "--envelope", alice.csrFile, "--key", alice.keyFile}},
		"submit, bad envelope": {context.Background(), []string{"barrier", "quorum", "submit", "--op", "unseal", "--username", "alice", "--envelope", alice.csrFile,
			"--challenge-id", "c1", "--factor", cliFIDO, "--response-file", response}},
	} {
		if out, err := runCLI(root(), tc.ctx, tc.args...); err == nil {
			t.Errorf("%s: answered %q, want a refusal", name, out)
		}
	}
}

// TestHolderCLI_RecoveryDomain: --domain recovery routes enrolment and both
// claim halves to the recovery holders, and the claim message the CLI prints
// for an external signer is bound to the recovery scope's label, not the
// unseal domain's.
func TestHolderCLI_RecoveryDomain(t *testing.T) {
	stack := newCLIHolderStack(t, 2, 3)
	root := func() *cobra.Command {
		return newTestRootCmdWithOpts(newMockKVService(), newMockAdminService(), embedded.WithBarrierService(stack.svc))
	}
	if got := (systemHolders{domain: transport.DomainRecovery}).claimLabel(); got != ceremony.ScopeRecovery.ClaimLabel() || got == ceremony.ScopeSystem.ClaimLabel() {
		t.Fatalf("recovery claim label = %q", got)
	}
	rita := newCLIHolder(t, "rita", transport.RoleCustodian, false, false)
	out := mustCLI(t, context.Background(), root, append([]string{"--output", "json", "barrier", "officer", "register", "--domain", "recovery"}, rita.registerArgs()...)...)
	var resp transport.OfficerRegisterResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("recovery register %q: %v", out, err)
	}
	if rita.cert, _ = mtls.ParseCertificatePEM([]byte(resp.CertPEM)); rita.cert == nil {
		t.Fatal("rita's certificate did not parse")
	}
	// Before custody is initialized there is no share to claim: both halves
	// reach the recovery service, and it refuses.
	if _, err := runCLI(root(), rita.ctx(), "barrier", "officer", "claim-begin", "--domain", "recovery", "--username", "rita"); err == nil {
		t.Fatal("a recovery claim before custody was answered")
	}
	sig := rita.write(t, "sig.bin", []byte("sig"))
	if _, err := runCLI(root(), rita.ctx(), "barrier", "officer", "claim-complete", "--domain", "recovery", "--username", "rita",
		"--nonce-hex", "ab", "--sig-file", sig, "--out", rita.envFile); err == nil {
		t.Fatal("a recovery claim completion before custody was answered")
	}
}
