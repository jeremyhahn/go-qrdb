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

package conformance

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// The share-holder conformance scenario. On every transport, against a real
// node, with real factor verification and real HPKE envelopes:
//
//   - the admin enrols two Security Officers and three key custodians the
//     same way, and initializes 3-of-5 Shamir custody over them;
//   - every holder claims its envelope over the transport, once;
//   - the node restarts SEALED, and three holders — two authenticating with
//     the mtls factor, one each with a FIDO2-style assertion factor, their
//     envelopes sealed under four different KEMs — unseal it through the
//     sealed-window listener; a duplicate submission is refused, and a
//     non-designated factor is refused under the designated policy;
//   - revoking a holder is a quorum-gated rekey: the round does nothing until
//     M of the remaining holders submit, and the revoked holder is refused;
//   - a rekey to the any-enrolled policy lets a holder answer with a factor
//     other than its designated one.
//
// Holder identities come from the CA, never from the CSR: every CSR here
// asks for a common name the CA must ignore.

const (
	e2eFIDO      = "fido2"
	e2eThreshold = 3
	e2eTotal     = 5
	e2eKVKey     = "e2e/share-holders"
	e2eKVValue   = "survives-the-seal"
)

// e2eHolder is one share holder's client side: its certificate key, an
// optional separate envelope key, an optional assertion authenticator, and
// the certificate and envelope it has received.
type e2eHolder struct {
	name       string
	role       string
	designated string
	key        crypto.Signer
	csrPEM     string
	encKEM     string
	encPriv    []byte
	encPub     []byte
	fido       *factortest.Authenticator
	cert       *x509.Certificate
	certPEM    []byte
	envelope   []byte
}

// newE2EHolder builds a holder with a certificate key on curve, a separate
// envelope key of encKEM when non-empty, and an assertion authenticator (its
// designated factor) when fido is set.
func newE2EHolder(t *testing.T, name, role string, curve elliptic.Curve, encKEM string, fido bool) *e2eHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "requester-chosen-" + name, OrganizationalUnit: []string{"system", "security-officer"}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	h := &e2eHolder{
		name:       name,
		role:       role,
		designated: "mtls",
		key:        key,
		csrPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
		encKEM:     encKEM,
	}
	if encKEM != "" {
		suite, err := keydelivery.LookupSuite(encKEM)
		if err != nil {
			t.Fatal(err)
		}
		sk, err := suite.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if h.encPriv, err = sk.Bytes(); err != nil {
			t.Fatal(err)
		}
		h.encPub = sk.PublicKey().Bytes()
	}
	if fido {
		if h.fido, err = factortest.NewAuthenticator(); err != nil {
			t.Fatal(err)
		}
		h.designated = e2eFIDO
	}
	return h
}

// registerRequest is the admin's enrolment request for the holder.
func (h *e2eHolder) registerRequest() *transport.OfficerRegisterRequest {
	req := &transport.OfficerRegisterRequest{
		Username:      h.name,
		Role:          h.role,
		CSRPEM:        h.csrPEM,
		EncryptionKEM: h.encKEM,
		EncryptionKey: h.encPub,
		Designated:    h.designated,
	}
	if h.fido != nil {
		req.Factors = []transport.HolderFactorEnrolment{{Name: e2eFIDO, Params: h.fido.EnrollParams()}}
	}
	return req
}

// recordCert stores the CA-issued certificate, checking the CA assigned the
// identity rather than copying the CSR's.
func (h *e2eHolder) recordCert(t *testing.T, certPEM string) {
	t.Helper()
	cert, err := mtls.ParseCertificatePEM([]byte(certPEM))
	if err != nil {
		t.Fatalf("%s: parse issued certificate: %v", h.name, err)
	}
	if cert.Subject.CommonName != h.name {
		t.Fatalf("%s: issued CN = %q; the CA must assign the identity, not take the CSR's", h.name, cert.Subject.CommonName)
	}
	h.cert = cert
	h.certPEM = []byte(certPEM)
}

// tlsConfig is the holder's client TLS configuration: its own certificate.
func (h *e2eHolder) tlsConfig(pool *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{h.cert.Raw}, PrivateKey: h.key, Leaf: h.cert}},
		RootCAs:      pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}
}

// share opens the holder's claimed envelope with its own key.
func (h *e2eHolder) share(t *testing.T) []byte {
	t.Helper()
	env, err := ceremony.ParseShareEnvelope(h.envelope)
	if err != nil {
		t.Fatalf("%s: parse envelope: %v", h.name, err)
	}
	if h.encKEM == "" {
		share, err := env.OpenWithKey(h.key)
		if err != nil {
			t.Fatalf("%s: open envelope with the certificate key: %v", h.name, err)
		}
		return share
	}
	suite, err := keydelivery.LookupSuite(h.encKEM)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := suite.PrivateKey(h.encPriv)
	if err != nil {
		t.Fatal(err)
	}
	share, err := env.Open(sk)
	if err != nil {
		t.Fatalf("%s: open %s envelope: %v", h.name, h.encKEM, err)
	}
	return share
}

// answer produces the holder's response to a quorum challenge.
func (h *e2eHolder) answer(t *testing.T, ch *transport.QuorumChallenge) []byte {
	t.Helper()
	if ch.Factor == e2eFIDO {
		return h.fido.Assert(ch.Nonce)
	}
	sig, err := sdk.SignQuorumChallenge(h.key, ch.ChallengeID, ch.MethodData[sdk.QuorumChallengePurposeKey], ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// holderAPI is the share-holder surface a holder drives.
type holderAPI interface {
	ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error)
	ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error)
	BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error)
	BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error)
}

// sealedServiceHolders is the in-process holder surface of a SEALED node: a
// sealed node has no embedded client (there is no data plane to serve), so an
// embedded composer drives the unseal through the node's barrier service,
// exactly as the node documentation prescribes. The TLS peer, when the
// context carries one, is taken from the context as the embedded transport
// takes it.
type sealedServiceHolders struct{ svc service.BarrierServicer }

func peerFromContext(ctx context.Context) string {
	if st := auth.TLSStateFromContext(ctx); st != nil && len(st.PeerCertificates) > 0 {
		return mtls.Thumbprint(st.PeerCertificates[0].Raw)
	}
	return ""
}

func (s sealedServiceHolders) ShareClaimBegin(ctx context.Context, req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	ch, err := s.svc.BarrierShareClaimBegin(ctx, seal.DomainUnseal, req.Username, peerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimBeginResponse{NonceHex: ch.NonceHex, ExpiresAt: ch.ExpiresAt}, nil
}

func (s sealedServiceHolders) ShareClaimComplete(ctx context.Context, req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	res, err := s.svc.BarrierShareClaimComplete(ctx, seal.DomainUnseal, req.Username, req.NonceHex, req.Signature, peerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return &transport.ShareClaimCompleteResponse{ShareIndex: res.ShareIndex, Envelope: res.Envelope}, nil
}

func (s sealedServiceHolders) BarrierQuorumBegin(ctx context.Context, req *transport.QuorumBeginRequest) (*transport.QuorumChallenge, error) {
	ch, err := s.svc.BarrierQuorumBegin(ctx, holderwire.BeginFrom(req))
	if err != nil {
		return nil, err
	}
	return holderwire.ChallengeTo(ch), nil
}

func (s sealedServiceHolders) BarrierQuorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	out, err := s.svc.BarrierQuorumSubmit(ctx, holderwire.SubmitFrom(req, peerFromContext(ctx)))
	if err != nil {
		return nil, err
	}
	return holderwire.OutcomeTo(out), nil
}

// holderE2E is one transport's run of the scenario.
type holderE2E struct {
	t         *testing.T
	tk        transportKind
	cfg       *qrdbconfig.Config
	ca        *ceremony.LocalCA
	pool      *x509.CertPool
	serverTLS *tls.Config
	factors   *authfactors.FactorRegistry
	n         *node.Node
	holders   map[string]*e2eHolder
	adminTLS  *tls.Config
	// pins is the barrier PIN backend the composer injects, with the SO PIN
	// e2eSOPIN provisioned: rekey and revocation present it.
	pins pin.PINBackend
}

// e2eSOPIN is the barrier SO PIN the share-holder scenario provisions.
const e2eSOPIN = "so-pin-1234"

// newHolderE2E prepares the CA, server TLS, factor registry, holders and the
// node configuration for tk.
func newHolderE2E(t *testing.T, tk transportKind) *holderE2E {
	t.Helper()
	e := &holderE2E{t: t, tk: tk, holders: map[string]*e2eHolder{}}
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", e2eSOPIN); err != nil {
		t.Fatal(err)
	}
	e.pins = pins

	var caPEM []byte
	e.ca, caPEM = ceremonytest.NewCA(t, "Share Holder Conformance CA")
	var err error
	e.pool = x509.NewCertPool()
	e.pool.AppendCertsFromPEM(caPEM)

	if e.serverTLS, err = ceremony.ServerTLSConfig(context.Background(), e.ca, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	e.serverTLS.ClientCAs = e.pool
	e.serverTLS.ClientAuth = tls.VerifyClientCertIfGiven

	e.factors = authfactors.NewFactorRegistry()
	if err := e.factors.Register(factortest.New(e2eFIDO, time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Two Security Officers and three custodians, their envelopes sealed under
	// four different KEMs: to the P-256 and P-384 certificate keys, and to
	// separately enrolled X25519 and ML-KEM-768+X25519 keys.
	for _, h := range []*e2eHolder{
		newE2EHolder(t, "so-alice", transport.RoleSecurityOfficer, elliptic.P256(), "", false),
		newE2EHolder(t, "so-bob", transport.RoleSecurityOfficer, elliptic.P384(), "", false),
		newE2EHolder(t, "cust-carol", transport.RoleCustodian, elliptic.P256(), keydelivery.KEMX25519, true),
		newE2EHolder(t, "cust-dave", transport.RoleCustodian, elliptic.P256(), keydelivery.KEMMLKEM768X25519, true),
		newE2EHolder(t, "cust-erin", transport.RoleCustodian, elliptic.P256(), "", false),
	} {
		e.holders[h.name] = h
	}

	c := cell{backend: database.BackendPebble, mode: standalone, transport: tk, want: fullPassage}
	e.cfg = c.config(t)
	e.cfg.Seal.Enabled = true
	e.cfg.Seal.Strategy = "shamir"
	e.cfg.Seal.ShamirThreshold = e2eThreshold
	e.cfg.Seal.ShamirTotal = e2eTotal
	return e
}

// serverOpts are the composition options every boot and sealed window uses.
func (e *holderE2E) serverOpts() []server.ServerOption {
	return []server.ServerOption{
		server.WithTLS(e.serverTLS),
		server.WithCeremonyCA(e.ca),
		server.WithFactorRegistry(e.factors),
		server.WithPINBackend(e.pins),
	}
}

// boot starts the node over the scenario's data directory.
func (e *holderE2E) boot() {
	e.t.Helper()
	n, err := node.NewStandaloneNode(context.Background(), e.cfg, e.serverOpts())
	if err != nil {
		e.t.Fatalf("%s: boot: %v", e.tk, err)
	}
	e.n = n
	e.t.Cleanup(func() { _ = n.Close() })
}

// address is the listener address of the transport under test, from the
// configuration rather than the node: during the sealed window the node has
// no server of its own.
func (e *holderE2E) address() string {
	switch e.tk {
	case grpcTransport:
		return fmt.Sprintf("127.0.0.1:%d", e.cfg.Server.GRPCPort)
	case quicTransport:
		return fmt.Sprintf("127.0.0.1:%d", e.cfg.Server.QUICPort)
	case unixTransport:
		return e.cfg.Unix.SocketPath
	case restTransport:
		return fmt.Sprintf("https://127.0.0.1:%d", e.cfg.Server.AdminPort)
	case mcpTransport:
		return fmt.Sprintf("https://127.0.0.1:%d/mcp", e.cfg.MCP.Port)
	}
	e.t.Fatalf("transport %q has no listener", e.tk)
	return ""
}

// dial connects an SDK client of the transport under test with tlsCfg,
// retrying while a listener is still coming up.
func (e *holderE2E) dial(tlsCfg *tls.Config) *sdk.Client {
	e.t.Helper()
	opts := []transport.Option{transport.WithAddress(e.address()), transport.WithTLSConfig(tlsCfg)}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var (
			tr  transport.Transport
			err error
		)
		switch e.tk {
		case grpcTransport:
			tr, err = grpctransport.New(opts...)
		case quicTransport:
			tr, err = quictransport.New(opts...)
		case unixTransport:
			tr, err = unixtransport.New(opts...)
		case restTransport:
			tr, err = resttransport.New(opts...)
		case mcpTransport:
			tr, err = mcptransport.New(opts...)
		}
		if err != nil {
			e.t.Fatalf("%s: new transport: %v", e.tk, err)
		}
		client, err := sdk.NewClient(tr)
		if err != nil {
			e.t.Fatalf("%s: new SDK client: %v", e.tk, err)
		}
		if err = client.Connect(boundedCtx(e.t)); err == nil {
			e.t.Cleanup(func() { _ = client.Close() })
			return client
		}
		_ = client.Close()
		if time.Now().After(deadline) {
			e.t.Fatalf("%s: connect: %v", e.tk, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// holderClient returns h's holder surface over the transport under test and
// the context h's calls carry. sealed selects the sealed-window path.
func (e *holderE2E) holderClient(h *e2eHolder, sealed bool) (holderAPI, context.Context) {
	e.t.Helper()
	if e.tk == embeddedTransport {
		ctx := auth.WithTLSState(boundedCtx(e.t), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{h.cert}})
		if sealed {
			return sealedServiceHolders{svc: e.n.Services().Barrier}, ctx
		}
		return e.n.Client().Barrier(), ctx
	}
	return e.dial(h.tlsConfig(e.pool)).Barrier(), boundedCtx(e.t)
}

// adminClient returns the admin's barrier client over the transport under
// test, seeding the admin identity with the holder-administration grants.
func (e *holderE2E) adminClient() transport.BarrierClient {
	e.t.Helper()
	if e.tk == embeddedTransport {
		return e.n.Client().Barrier()
	}
	if e.adminTLS == nil {
		svc := e.n.Services()
		ctx := boundedCtx(e.t)
		r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
			Name:     "conformance-holder-admin",
			TenantID: tenantid.System,
			Permissions: []permission.Permission{
				permission.SealOfficerList, permission.SealOfficerRevoke, permission.SealRekey,
				permission.SealStatus, permission.AdminHealth, permission.AdminStatus,
			},
		})
		if err != nil {
			e.t.Fatalf("create role: %v", err)
		}
		adminUser, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
			Username: "holder-admin",
			TenantID: tenantid.System,
			RoleIDs:  []string{r.ID},
		})
		if err != nil {
			e.t.Fatalf("create user: %v", err)
		}
		issued, err := e.ca.IssueCertificate(ctx, &ceremony.IssueCertRequest{
			CommonName:   "holder-admin",
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			ValidityDays: 1,
		})
		if err != nil {
			e.t.Fatal(err)
		}
		// auth.mtls_binding_policy defaults to "required" (go-qrdb#548): this
		// identity is a real RBAC account (SealOfficerRevoke etc.), not a
		// holder, so it needs an enrolled binding to authenticate at all --
		// the same convention enrollMatrixCert uses for "matrix".
		if _, err := svc.User.AddCertBinding(ctx, adminUser.ID, user.ComputeSPKIThumbprint(issued.Certificate), 0); err != nil {
			e.t.Fatalf("enrol holder-admin cert binding: %v", err)
		}
		cert, err := tls.X509KeyPair(issued.CertPEM, issued.KeyPEM)
		if err != nil {
			e.t.Fatal(err)
		}
		e.adminTLS = &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: e.pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13}
	}
	return e.dial(e.adminTLS).Barrier()
}

// enrolAndInitialize has the admin enrol every holder and initialize 3-of-5
// custody, in-process: an uninitialized node has no RBAC store yet, so this
// is the embedded composer's path (the remote path is qrdb init).
//
// BarrierOfficerRegister still goes through e.svc (e.n.Services().Barrier):
// registration is unaffected by go-qrdb#560 review item 8. The Shamir
// initialization itself, though, can no longer go through
// BarrierService.BarrierInitializeShamir on this ceremony-backed service --
// it now unconditionally refuses with *SystemBarrierInitRefusedError,
// exactly as BarrierInitialize already did, because qrdb init is the only
// system bootstrap. It goes through the ceremony's own InitializeShamir
// instead, the same holder-authority path qrdb init itself uses, over the
// same underlying barrier.
func (e *holderE2E) enrolAndInitialize() {
	e.t.Helper()
	svc := e.n.Services().Barrier
	ctx := boundedCtx(e.t)
	for _, name := range e.names() {
		h := e.holders[name]
		res, err := svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, holderwire.RegisterFrom(h.registerRequest()))
		if err != nil {
			e.t.Fatalf("register %s: %v", name, err)
		}
		h.recordCert(e.t, holderwire.RegisterTo(res).CertPEM)
	}
	core, ok := service.CeremonyServiceCore(e.n.Services().Ceremony)
	if !ok {
		e.t.Fatal("node's CeremonyServicer has no ceremony core")
	}
	barrier := e.n.BarrierRegistry().SystemBarrier()
	barrier.SetShamirConfig(&seal.ShamirConfig{Threshold: e2eThreshold, TotalShares: e2eTotal})
	params, err := core.InitializeShamir(ctx, seal.FactorPolicyDesignated)
	if err != nil {
		e.t.Fatalf("initialize shamir: %v", err)
	}
	if params.ShareCount != e2eTotal {
		e.t.Fatalf("shares distributed = %d, want %d", params.ShareCount, e2eTotal)
	}
}

// names returns the holder names in a stable order.
func (e *holderE2E) names() []string {
	out := make([]string, 0, len(e.holders))
	for name := range e.holders {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// claim runs h's claim handshake over the transport under test.
func (e *holderE2E) claim(h *e2eHolder) error {
	e.t.Helper()
	api, ctx := e.holderClient(h, false)
	begin, err := api.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: h.name})
	if err != nil {
		return err
	}
	nonce, err := hex.DecodeString(begin.NonceHex)
	if err != nil {
		e.t.Fatal(err)
	}
	sig, err := sdk.SignShareClaim(h.key, sdk.SystemClaimLabel, h.name, nonce)
	if err != nil {
		e.t.Fatal(err)
	}
	done, err := api.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: h.name, NonceHex: begin.NonceHex, Signature: sig})
	if err != nil {
		return err
	}
	h.envelope = done.Envelope
	return nil
}

// claimAll claims every named holder's envelope, failing on any refusal.
func (e *holderE2E) claimAll(names ...string) {
	e.t.Helper()
	for _, name := range names {
		if err := e.claim(e.holders[name]); err != nil {
			e.t.Fatalf("%s: claim %s: %v", e.tk, name, err)
		}
	}
}

// submit authenticates h with factor (its designated one when empty) and
// submits its share to op.
func (e *holderE2E) submit(h *e2eHolder, op, factor string, sealed bool) (*transport.QuorumOutcome, error) {
	e.t.Helper()
	api, ctx := e.holderClient(h, sealed)
	ch, err := api.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: transport.DomainUnseal, Op: op, Username: h.name, Factor: factor})
	if err != nil {
		return nil, err
	}
	return api.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{
		Domain:      transport.DomainUnseal,
		Op:          op,
		Username:    h.name,
		ChallengeID: ch.ChallengeID,
		Factor:      ch.Factor,
		Response:    h.answer(e.t, ch),
		Share:       h.share(e.t),
	})
}

// mustSubmit submits and fails the test on a refusal.
func (e *holderE2E) mustSubmit(name, op, factor string, sealed bool) *transport.QuorumOutcome {
	e.t.Helper()
	out, err := e.submit(e.holders[name], op, factor, sealed)
	if err != nil {
		e.t.Fatalf("%s: %s %s: %v", e.tk, name, op, err)
	}
	return out
}

// restartSealed closes the node and boots it again; Shamir never
// auto-unseals, so it comes back sealed. For a remote transport the
// sealed-window listener is brought up for the holders.
func (e *holderE2E) restartSealed() <-chan error {
	e.t.Helper()
	if err := e.n.Close(); err != nil {
		e.t.Fatalf("close: %v", err)
	}
	e.boot()
	if !e.n.IsSealed() {
		e.t.Fatalf("%s: a Shamir node must restart sealed", e.tk)
	}
	done := make(chan error, 1)
	if e.tk == embeddedTransport {
		done <- nil
		return done
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	e.t.Cleanup(cancel)
	// The sealed window serves the node's own seal services, which already
	// carry the factor registry; it needs only the listener's TLS and CA.
	go func() {
		done <- node.ServeSealedWindow(ctx, e.n, server.WithTLS(e.serverTLS), server.WithCeremonyCA(e.ca))
	}()
	return done
}

// holderStillEnrolled reports whether name is on the unseal roster.
func holderStillEnrolled(t *testing.T, admin transport.BarrierClient, name string) bool {
	t.Helper()
	officers, err := admin.OfficerList(boundedCtx(t))
	if err != nil {
		t.Fatalf("officer list: %v", err)
	}
	return slices.ContainsFunc(officers, func(o *transport.OfficerStatus) bool { return o.Username == name })
}

// TestShareHolders_ConformAcrossEverySDKTransport is the unified share-holder
// scenario on every SDK transport.
func TestShareHolders_ConformAcrossEverySDKTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			e := newHolderE2E(t, tk)

			// --- Enrolment, initialization and the first boot. -----------------
			e.boot()
			if !e.n.IsSealed() {
				t.Fatal("an uninitialized Shamir node boots sealed")
			}
			e.enrolAndInitialize()
			if err := e.n.Unseal(boundedCtx(t), seal.Credentials{}); err != nil {
				t.Fatalf("complete boot: %v", err)
			}
			if err := e.n.KVService().Put(boundedCtx(t), e2eKVKey, []byte(e2eKVValue)); err != nil {
				t.Fatalf("seed data: %v", err)
			}

			// --- Every holder claims its envelope, once. -------------------------
			e.claimAll(e.names()...)
			if err := e.claim(e.holders["so-alice"]); err == nil {
				t.Fatal("a second claim of the same envelope was answered")
			}

			// --- Restart sealed; three holders unseal. ---------------------------
			window := e.restartSealed()
			first := e.mustSubmit("so-alice", transport.OpUnseal, "", true)
			if first.Complete || first.Submitted != 1 || first.Required != e2eThreshold {
				t.Fatalf("after one holder: %+v", first)
			}
			if _, err := e.submit(e.holders["so-alice"], transport.OpUnseal, "", true); err == nil {
				t.Fatal("a holder's second submission to the same round was counted")
			}
			// The designated policy admits only carol's enrolled FIDO2 factor.
			if _, err := e.submit(e.holders["cust-carol"], transport.OpUnseal, "mtls", true); err == nil {
				t.Fatal("a non-designated factor was admitted under the designated policy")
			}
			if out := e.mustSubmit("cust-carol", transport.OpUnseal, "", true); out.Complete || out.Submitted != 2 {
				t.Fatalf("after two holders: %+v", out)
			}
			if out := e.mustSubmit("cust-dave", transport.OpUnseal, "", true); !out.Complete {
				t.Fatalf("three holders did not unseal: %+v", out)
			}
			if err := <-window; err != nil {
				t.Fatalf("sealed window: %v", err)
			}
			if err := e.n.Unseal(boundedCtx(t), seal.Credentials{}); err != nil {
				t.Fatalf("complete boot after unseal: %v", err)
			}
			got, err := e.n.KVService().Get(boundedCtx(t), e2eKVKey)
			if err != nil || string(got) != e2eKVValue {
				t.Fatalf("data after the holders' unseal = %q, %v", got, err)
			}

			// --- Revoking a holder is a quorum-gated rekey. ------------------------
			admin := e.adminClient()
			round, err := admin.BarrierOfficerRevoke(boundedCtx(t), &transport.OfficerRevokeRequest{Domain: transport.DomainUnseal, Username: "cust-erin", SOPin: e2eSOPIN})
			if err != nil {
				t.Fatalf("revoke: %v", err)
			}
			if round.Required != e2eThreshold || !slices.Contains(round.Remove, "cust-erin") {
				t.Fatalf("revocation round = %+v", round)
			}
			if _, err := e.submit(e.holders["cust-erin"], transport.OpRekey, "", false); err == nil {
				t.Fatal("the holder being revoked contributed to its own revocation")
			}
			e.mustSubmit("so-alice", transport.OpRekey, "", false)
			e.mustSubmit("so-bob", transport.OpRekey, "", false)
			if !holderStillEnrolled(t, admin, "cust-erin") {
				t.Fatal("revocation took effect before M holders submitted")
			}
			if out := e.mustSubmit("cust-carol", transport.OpRekey, "", false); !out.Complete || out.TotalShares != e2eTotal-1 {
				t.Fatalf("revocation rekey outcome = %+v", out)
			}
			if holderStillEnrolled(t, admin, "cust-erin") {
				t.Fatal("the revoked holder is still on the roster")
			}
			e.claimAll("cust-carol", "cust-dave", "so-alice", "so-bob")
			if err := e.claim(e.holders["cust-erin"]); err == nil {
				t.Fatal("a revoked holder claimed an envelope")
			}

			// --- A rekey to the any-enrolled policy. ------------------------------
			if _, err := admin.BarrierRekeyStart(boundedCtx(t), &transport.RekeyStartRequest{
				Domain: transport.DomainUnseal, Threshold: e2eThreshold, Policy: transport.PolicyAnyEnrolled, SOPin: e2eSOPIN,
			}); err != nil {
				t.Fatalf("rekey start: %v", err)
			}
			for _, name := range []string{"so-alice", "so-bob", "cust-dave"} {
				e.mustSubmit(name, transport.OpRekey, "", false)
			}
			e.claimAll("cust-carol", "cust-dave", "so-alice", "so-bob")
			// Under any-enrolled, carol may answer with mtls instead of FIDO2.
			e.mustSubmit("cust-carol", transport.OpRootToken, "mtls", false)
			e.mustSubmit("so-alice", transport.OpRootToken, "", false)
			token := e.mustSubmit("so-bob", transport.OpRootToken, "", false)
			if !token.Complete || token.RootToken == "" {
				t.Fatalf("root-token quorum outcome = %+v", token)
			}
		})
	}
}

// TestShareHolders_RefusedForNonHolderVerb is the other half of the
// go-qrdb#548 carve-out: a holder certificate authenticates (it is ALWAYS
// admitted, regardless of auth.mtls_binding_policy -- a holder is by design
// never an account, so the "required" default's account-enrolment rationale
// does not reach it), but it carries no roles, so RBAC refuses it every
// permission that is not permission.IdentityGated. AdminStatus is an
// arbitrary ordinary example, not a holder verb.
func TestShareHolders_RefusedForNonHolderVerb(t *testing.T) {
	e := newHolderE2E(t, restTransport)
	e.boot()
	e.enrolAndInitialize()
	if err := e.n.Unseal(boundedCtx(t), seal.Credentials{}); err != nil {
		t.Fatalf("complete boot: %v", err)
	}
	h := e.holders["so-alice"]
	client := e.dial(h.tlsConfig(e.pool))
	_, err := client.Admin().Status(boundedCtx(t))
	if err == nil {
		t.Fatal("a holder certificate was admitted for admin:status, a non-holder verb")
	}
	t.Logf("admin:status refused as expected: %v", err)
}

// TestShareHolders_CannotRegisterAnotherHolder proves the go-qrdb#548
// carve-out does not extend registration to a holder cert. Enrolling a NEW
// holder (OfficerRegister) is a real, registered RBAC permission
// (seal:officer_register) requiring an Admin or Security Officer role grant
// -- never permission.IdentityGated -- so a holder certificate, which always
// authenticates but always carries an empty role set, must be refused here
// exactly as for any other non-holder verb (TestShareHolders_
// RefusedForNonHolderVerb). A holder can only ever complete its own
// enrolment or claim its own share; it must never be able to seat a second
// holder on the roster.
func TestShareHolders_CannotRegisterAnotherHolder(t *testing.T) {
	e := newHolderE2E(t, restTransport)
	e.boot()
	e.enrolAndInitialize()
	if err := e.n.Unseal(boundedCtx(t), seal.Credentials{}); err != nil {
		t.Fatalf("complete boot: %v", err)
	}
	h := e.holders["so-alice"]
	client := e.dial(h.tlsConfig(e.pool))

	intruder := newE2EHolder(t, "so-mallory", transport.RoleSecurityOfficer, elliptic.P256(), "", false)
	_, err := client.Barrier().OfficerRegister(boundedCtx(t), intruder.registerRequest())
	if err == nil {
		t.Fatal("a holder certificate registered a new holder; OfficerRegister must require an Admin or SO role, never IdentityGated")
	}
	t.Logf("officer:register refused as expected: %v", err)
}

// TestShareHolders_UnregisteredHolderCertRefusedByRoster proves the
// go-qrdb#548 carve-out is not a bypass: it only ever admits the identity
// into the middleware chain. A certificate the ceremony CA signs with a
// holder profile, for a username NEVER enrolled through
// BarrierOfficerRegister, authenticates exactly like any other holder
// certificate -- then the officer-registry lookup ShareClaimBegin makes
// refuses it, because possession of a holder-shaped certificate is not
// possession of an enrolled share; the roster entry (by thumbprint) is the
// actual binding, same as the existing claim/revocation refusals this test
// package already covers for a REVOKED holder (TestShareHolders_
// ConformAcrossEverySDKTransport's "cust-erin" assertions). This is the
// unregistered case: an officer that was never on the roster at all.
func TestShareHolders_UnregisteredHolderCertRefusedByRoster(t *testing.T) {
	e := newHolderE2E(t, restTransport)
	e.boot()
	e.enrolAndInitialize()
	if err := e.n.Unseal(boundedCtx(t), seal.Credentials{}); err != nil {
		t.Fatalf("complete boot: %v", err)
	}

	mallory := newE2EHolder(t, "so-mallory", transport.RoleSecurityOfficer, elliptic.P256(), "", false)
	block, _ := pem.Decode([]byte(mallory.csrPEM))
	if block == nil {
		t.Fatal("mallory's own CSR did not encode")
	}
	cert, err := e.ca.SignCSR(boundedCtx(t), []byte(mallory.csrPEM), &ceremony.SignCSROptions{
		TenantID:     tenantid.System,
		Username:     mallory.name,
		Profile:      "security-officer",
		ValidityDays: 1,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("sign mallory's holder certificate directly, bypassing BarrierOfficerRegister: %v", err)
	}
	mallory.cert = cert
	mallory.certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})

	if err := e.claim(mallory); err == nil {
		t.Fatal("a holder certificate for a username never enrolled through BarrierOfficerRegister claimed an envelope")
	}
}
