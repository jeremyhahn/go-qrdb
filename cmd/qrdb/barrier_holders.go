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
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Share holders — Security Officers and key custodians — are enrolled, claim
// their sealed share envelopes, and submit to quorum rounds through the same
// verbs on the system barrier (`barrier officer|quorum|rekey`), on a tenant
// barrier (`tenant barrier-officer|barrier-quorum|barrier-rekey`) and on a
// database barrier (`database barrier-officer|barrier-quorum|barrier-rekey`).
// holderTarget is the one seam between them: every verb is written once
// against it.

// holderTarget is the barrier a holder verb acts on.
type holderTarget interface {
	register(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error)
	list(ctx context.Context) ([]*transport.OfficerStatus, error)
	claimBegin(ctx context.Context, username string) (*transport.ShareClaimBeginResponse, error)
	claimComplete(ctx context.Context, username, nonceHex string, signature []byte) (*transport.ShareClaimCompleteResponse, error)
	quorumBegin(ctx context.Context, op, username, factor string) (*transport.QuorumChallenge, error)
	quorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error)
	rekey(ctx context.Context, threshold int, policy, soPin string) (*transport.QuorumRound, error)
	revoke(ctx context.Context, username string, threshold int, soPin string) (*transport.QuorumRound, error)
}

// systemHolders is the system barrier, in one quorum domain.
type systemHolders struct {
	cl     *sdk.Client
	domain string
}

func (s systemHolders) register(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	if s.domain == transport.DomainRecovery {
		return s.cl.Barrier().RecoveryOfficerRegister(ctx, req)
	}
	return s.cl.Barrier().OfficerRegister(ctx, req)
}

func (s systemHolders) list(ctx context.Context) ([]*transport.OfficerStatus, error) {
	if s.domain == transport.DomainRecovery {
		return nil, &InvalidArgError{Arg: "--domain", Reason: "listing is served for the unseal domain"}
	}
	return s.cl.Barrier().OfficerList(ctx)
}

func (s systemHolders) claimBegin(ctx context.Context, username string) (*transport.ShareClaimBeginResponse, error) {
	req := &transport.ShareClaimBeginRequest{Username: username}
	if s.domain == transport.DomainRecovery {
		return s.cl.Barrier().RecoveryShareClaimBegin(ctx, req)
	}
	return s.cl.Barrier().ShareClaimBegin(ctx, req)
}

func (s systemHolders) claimComplete(ctx context.Context, username, nonceHex string, signature []byte) (*transport.ShareClaimCompleteResponse, error) {
	req := &transport.ShareClaimCompleteRequest{Username: username, NonceHex: nonceHex, Signature: signature}
	if s.domain == transport.DomainRecovery {
		return s.cl.Barrier().RecoveryShareClaimComplete(ctx, req)
	}
	return s.cl.Barrier().ShareClaimComplete(ctx, req)
}

func (s systemHolders) quorumBegin(ctx context.Context, op, username, factor string) (*transport.QuorumChallenge, error) {
	return s.cl.Barrier().BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: s.domain, Op: op, Username: username, Factor: factor})
}

func (s systemHolders) quorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	req.Domain = s.domain
	return s.cl.Barrier().BarrierQuorumSubmit(ctx, req)
}

func (s systemHolders) rekey(ctx context.Context, threshold int, policy, soPin string) (*transport.QuorumRound, error) {
	return s.cl.Barrier().BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: s.domain, Threshold: threshold, Policy: policy, SOPin: soPin})
}

func (s systemHolders) revoke(ctx context.Context, username string, threshold int, soPin string) (*transport.QuorumRound, error) {
	return s.cl.Barrier().BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: s.domain, Username: username, Threshold: threshold, SOPin: soPin})
}

// tenantHolders is a tenant barrier. It has one quorum domain, and its rekey
// and revocation are authorized by RBAC alone.
type tenantHolders struct {
	cl       *sdk.Client
	tenantID string
}

func (s tenantHolders) register(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	return s.cl.Tenants().TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{
		TenantID:      s.tenantID,
		Username:      req.Username,
		Role:          req.Role,
		CSRPEM:        req.CSRPEM,
		EncryptionKEM: req.EncryptionKEM,
		EncryptionKey: req.EncryptionKey,
		Factors:       req.Factors,
		Designated:    req.Designated,
	})
}

func (s tenantHolders) list(ctx context.Context) ([]*transport.OfficerStatus, error) {
	return s.cl.Tenants().TenantBarrierOfficerList(ctx, s.tenantID)
}

func (s tenantHolders) claimBegin(ctx context.Context, username string) (*transport.ShareClaimBeginResponse, error) {
	return s.cl.Tenants().TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: s.tenantID, Username: username})
}

func (s tenantHolders) claimComplete(ctx context.Context, username, nonceHex string, signature []byte) (*transport.ShareClaimCompleteResponse, error) {
	return s.cl.Tenants().TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{
		TenantID: s.tenantID, Username: username, NonceHex: nonceHex, Signature: signature,
	})
}

func (s tenantHolders) quorumBegin(ctx context.Context, op, username, factor string) (*transport.QuorumChallenge, error) {
	return s.cl.Tenants().TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: s.tenantID, Op: op, Username: username, Factor: factor})
}

func (s tenantHolders) quorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	return s.cl.Tenants().TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{
		TenantID:         s.tenantID,
		Op:               req.Op,
		Username:         req.Username,
		ChallengeID:      req.ChallengeID,
		Factor:           req.Factor,
		Response:         req.Response,
		ResponseMetadata: req.ResponseMetadata,
		Share:            req.Share,
	})
}

func (s tenantHolders) rekey(ctx context.Context, threshold int, policy, _ string) (*transport.QuorumRound, error) {
	return s.cl.Tenants().TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: s.tenantID, Threshold: threshold, Policy: policy})
}

func (s tenantHolders) revoke(ctx context.Context, username string, threshold int, _ string) (*transport.QuorumRound, error) {
	return s.cl.Tenants().TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: s.tenantID, Username: username, Threshold: threshold})
}

// databaseHolders is a database's barrier, resolved in the caller's tenant.
// Like a tenant barrier it has one quorum domain, and its rekey and
// revocation are authorized by RBAC alone.
type databaseHolders struct {
	cl       *sdk.Client
	database string
}

func (s databaseHolders) register(ctx context.Context, req *transport.OfficerRegisterRequest) (*transport.OfficerRegisterResponse, error) {
	return s.cl.Database().DatabaseBarrierOfficerRegister(ctx, &transport.DatabaseOfficerRegisterRequest{Database: s.database, OfficerRegisterRequest: *req})
}

func (s databaseHolders) list(ctx context.Context) ([]*transport.OfficerStatus, error) {
	return s.cl.Database().DatabaseBarrierOfficerList(ctx, s.database)
}

func (s databaseHolders) claimBegin(ctx context.Context, username string) (*transport.ShareClaimBeginResponse, error) {
	return s.cl.Database().DatabaseBarrierShareClaimBegin(ctx, &transport.DatabaseShareClaimBeginRequest{
		Database: s.database, ShareClaimBeginRequest: transport.ShareClaimBeginRequest{Username: username},
	})
}

func (s databaseHolders) claimComplete(ctx context.Context, username, nonceHex string, signature []byte) (*transport.ShareClaimCompleteResponse, error) {
	return s.cl.Database().DatabaseBarrierShareClaimComplete(ctx, &transport.DatabaseShareClaimCompleteRequest{
		Database:                  s.database,
		ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{Username: username, NonceHex: nonceHex, Signature: signature},
	})
}

func (s databaseHolders) quorumBegin(ctx context.Context, op, username, factor string) (*transport.QuorumChallenge, error) {
	return s.cl.Database().DatabaseBarrierQuorumBegin(ctx, &transport.DatabaseQuorumBeginRequest{
		Database: s.database, QuorumBeginRequest: transport.QuorumBeginRequest{Op: op, Username: username, Factor: factor},
	})
}

func (s databaseHolders) quorumSubmit(ctx context.Context, req *transport.QuorumSubmitRequest) (*transport.QuorumOutcome, error) {
	return s.cl.Database().DatabaseBarrierQuorumSubmit(ctx, &transport.DatabaseQuorumSubmitRequest{Database: s.database, QuorumSubmitRequest: *req})
}

func (s databaseHolders) rekey(ctx context.Context, threshold int, policy, _ string) (*transport.QuorumRound, error) {
	return s.cl.Database().DatabaseBarrierRekeyStart(ctx, &transport.DatabaseRekeyStartRequest{Database: s.database, Threshold: threshold, Policy: policy})
}

func (s databaseHolders) revoke(ctx context.Context, username string, threshold int, _ string) (*transport.QuorumRound, error) {
	return s.cl.Database().DatabaseBarrierOfficerRevoke(ctx, &transport.DatabaseOfficerRevokeRequest{Database: s.database, Username: username, Threshold: threshold})
}

// initialize splits the database's root key to its enrolled holders at the
// M-of-N the database was created with.
func (s databaseHolders) initialize(ctx context.Context, policy string) (*transport.BarrierInitializeShamirResponse, error) {
	return s.cl.Database().DatabaseBarrierShamirInitialize(ctx, &transport.DatabaseShamirInitializeRequest{Database: s.database, Policy: policy})
}

// holderKind is the kind of barrier a holder verb acts on.
type holderKind int

const (
	// holderSystem is the system barrier, in a quorum domain.
	holderSystem holderKind = iota
	// holderTenant is a tenant's barrier.
	holderTenant
	// holderDatabase is a database's barrier.
	holderDatabase
)

// soPinGated reports whether the kind's rekey and revocation take the SO
// PIN. Only the system barrier's do; a tenant's or a database's are
// authorized by RBAC alone.
func (k holderKind) soPinGated() bool { return k == holderSystem }

// holderScope binds the flag that selects a holderTarget of its kind:
// --domain for the system barrier, --tenant-id for a tenant barrier,
// --database for a database barrier.
type holderScope struct {
	kind  holderKind
	value string
}

// holderScopeFlags registers each kind's scope flag.
var holderScopeFlags = map[holderKind]func(s *holderScope, cmd *cobra.Command){
	holderSystem: func(s *holderScope, cmd *cobra.Command) {
		cmd.Flags().StringVar(&s.value, "domain", transport.DomainUnseal, "Quorum domain: unseal or recovery")
	},
	holderTenant: func(s *holderScope, cmd *cobra.Command) {
		cmd.Flags().StringVar(&s.value, "tenant-id", "", "Tenant whose barrier the holder belongs to")
		_ = cmd.MarkFlagRequired("tenant-id")
	},
	holderDatabase: func(s *holderScope, cmd *cobra.Command) {
		cmd.Flags().StringVar(&s.value, "database", "", "Database whose barrier the holder belongs to")
		_ = cmd.MarkFlagRequired("database")
	},
}

// holderTargets resolves each kind's holderTarget over cl.
var holderTargets = map[holderKind]func(cl *sdk.Client, value string) (holderTarget, error){
	holderSystem: func(cl *sdk.Client, domain string) (holderTarget, error) {
		if domain != transport.DomainUnseal && domain != transport.DomainRecovery {
			return nil, &InvalidArgError{Arg: "--domain", Reason: "must be unseal or recovery"}
		}
		return systemHolders{cl: cl, domain: domain}, nil
	},
	holderTenant: func(cl *sdk.Client, tenantID string) (holderTarget, error) {
		return tenantHolders{cl: cl, tenantID: tenantID}, nil
	},
	holderDatabase: func(cl *sdk.Client, database string) (holderTarget, error) {
		return databaseHolders{cl: cl, database: database}, nil
	},
}

// bind registers the scope flag on cmd.
func (s *holderScope) bind(cmd *cobra.Command) {
	holderScopeFlags[s.kind](s, cmd)
}

// target resolves the selected holderTarget.
func (s *holderScope) target() (holderTarget, error) {
	return holderTargets[s.kind](client, s.value)
}

// ---------------------------------------------------------------------------
// Key material
// ---------------------------------------------------------------------------

// readSigner loads the holder's certificate private key.
func readSigner(path string) (crypto.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &HolderFileError{Path: path, Err: err}
	}
	defer secure.Wipe(raw)
	return mtls.ParseSignerPEM(raw)
}

// signHexNonce signs a hex nonce with the holder's certificate key.
func signHexNonce(signer crypto.Signer, nonceHex string) ([]byte, error) {
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return nil, &InvalidArgError{Arg: "nonce", Reason: "not hex"}
	}
	return mtls.SignNonce(signer, nonce)
}

// openEnvelope opens a claimed envelope: with the separately enrolled HPKE
// key encKey (raw, in the envelope's suite encoding) when one is given, else
// with the certificate key.
func openEnvelope(raw, encKey []byte, certKey crypto.Signer) ([]byte, error) {
	env, err := ceremony.ParseShareEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if len(encKey) == 0 {
		if certKey == nil {
			return nil, &MissingArgError{Arg: "certificate key or envelope key"}
		}
		return env.OpenWithKey(certKey)
	}
	suite, err := keydelivery.LookupSuite(env.KEM)
	if err != nil {
		return nil, err
	}
	sk, err := suite.PrivateKey(encKey)
	if err != nil {
		return nil, err
	}
	return env.Open(sk)
}

// HolderFileError is returned when a holder key, CSR or envelope file
// cannot be read or written.
type HolderFileError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *HolderFileError) Error() string {
	return "qrdb: " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HolderFileError) Unwrap() error { return e.Err }

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

// holderRegisterFlags are the enrolment flags shared by every register verb.
type holderRegisterFlags struct {
	username   string
	role       string
	csrFile    string
	encKEM     string
	encKeyFile string
	designated string
	factors    []string
}

// bind registers the enrolment flags on cmd.
func (f *holderRegisterFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.username, "username", "", "Holder username (becomes the certificate CN)")
	cmd.Flags().StringVar(&f.role, "role", transport.RoleSecurityOfficer, "Holder role: so or custodian")
	cmd.Flags().StringVar(&f.csrFile, "csr-file", "", "PEM CSR supplying the holder's public key")
	cmd.Flags().StringVar(&f.encKEM, "encryption-kem", "", "HPKE suite of a separate envelope key ("+strings.Join(keydelivery.SuiteNames(), ", ")+")")
	cmd.Flags().StringVar(&f.encKeyFile, "encryption-key-file", "", "Raw public key of the separate envelope key, in the suite's encoding")
	cmd.Flags().StringVar(&f.designated, "designated", "", "Factor fixed at enrolment under the designated policy (default mtls)")
	cmd.Flags().StringArrayVar(&f.factors, "factor", nil, "Additional factor enrolment: name or name=<json params> (repeatable)")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("csr-file")
}

// request builds the registration request from the flags.
func (f *holderRegisterFlags) request() (*transport.OfficerRegisterRequest, error) {
	csr, err := os.ReadFile(f.csrFile)
	if err != nil {
		return nil, &HolderFileError{Path: f.csrFile, Err: err}
	}
	req := &transport.OfficerRegisterRequest{
		Username:      f.username,
		Role:          f.role,
		CSRPEM:        string(csr),
		EncryptionKEM: f.encKEM,
		Designated:    f.designated,
	}
	if f.encKeyFile != "" {
		key, err := os.ReadFile(f.encKeyFile)
		if err != nil {
			return nil, &HolderFileError{Path: f.encKeyFile, Err: err}
		}
		req.EncryptionKey = key
	}
	for _, spec := range f.factors {
		name, params, hasParams := strings.Cut(spec, "=")
		enrolment := transport.HolderFactorEnrolment{Name: name}
		if hasParams {
			if err := json.Unmarshal([]byte(params), &enrolment.Params); err != nil {
				return nil, &InvalidArgError{Arg: "--factor", Reason: name + " params are not a JSON object"}
			}
		}
		req.Factors = append(req.Factors, enrolment)
	}
	return req, nil
}

// ---------------------------------------------------------------------------
// Verb constructors
// ---------------------------------------------------------------------------

// newHolderOfficerCmd builds the officer group for a scope.
func newHolderOfficerCmd(use, short string, kind holderKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: `Share holders — Security Officers and key custodians — are enrolled the same
way. The node's CA issues each holder a certificate whose identity it assigns;
nobody may enrol themselves. After Shamir initialization (or a rekey) each
holder claims its HPKE-sealed share envelope once, with its certificate, and
opens it offline with its own key.`,
	}
	cmd.AddCommand(
		newHolderRegisterCmd(kind),
		newHolderListCmd(kind),
		newHolderClaimCmd(kind),
		newHolderClaimBeginCmd(kind),
		newHolderClaimCompleteCmd(kind),
		newHolderRevokeCmd(kind),
	)
	return cmd
}

// newHolderRegisterCmd returns `register`.
func newHolderRegisterCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	f := &holderRegisterFlags{}
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Enrol a share holder",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			req, err := f.request()
			if err != nil {
				return err
			}
			resp, err := target.register(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderHolder(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Role:        %s\n", resp.Role)
				_, _ = fmt.Fprintf(outputWriter, "Designated:  %s\n", resp.Designated)
				_, _ = fmt.Fprintf(outputWriter, "EnvelopeKEM: %s\n", resp.EncryptionKEM)
				_, _ = fmt.Fprintf(outputWriter, "Factors:     %s\n", strings.Join(resp.Factors, ","))
				_, _ = fmt.Fprintf(outputWriter, "CertPEM:\n%s\n", resp.CertPEM)
			})
		},
	}
	scope.bind(cmd)
	f.bind(cmd)
	return cmd
}

// newHolderListCmd returns `list`.
func newHolderListCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List share holders and their claim state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			officers, err := target.list(cmd.Context())
			if err != nil {
				return err
			}
			return renderHolder(officers, func() {
				for _, o := range officers {
					claimed := "no"
					if !o.ClaimedAt.IsZero() {
						claimed = o.ClaimedAt.Format("2006-01-02T15:04:05Z")
					}
					_, _ = fmt.Fprintf(outputWriter, "- %-20s role=%s index=%d designated=%s factors=%s pending=%v claimed=%s\n",
						o.Username, o.Role, o.ShareIndex, o.Designated, strings.Join(o.Factors, ","), o.SharePending, claimed)
				}
			})
		},
	}
	scope.bind(cmd)
	return cmd
}

// writeEnvelope writes a claimed envelope to path, readable by the owner only.
func writeEnvelope(path string, resp *transport.ShareClaimCompleteResponse) error {
	if err := os.WriteFile(path, resp.Envelope, 0o600); err != nil {
		return &HolderFileError{Path: path, Err: err}
	}
	return renderHolder(resp, func() {
		_, _ = fmt.Fprintf(outputWriter, "ShareIndex: %d\n", resp.ShareIndex)
		_, _ = fmt.Fprintf(outputWriter, "Envelope:   %s\n", path)
	})
}

// newHolderClaimCmd returns `claim`: the whole handshake, signed with the
// holder's certificate key.
func newHolderClaimCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	var username, keyFile, out string
	cmd := &cobra.Command{
		Use:   "claim",
		Short: "Claim a holder's sealed share envelope (once)",
		Long: `Run the claim handshake with the holder's certificate key and write the
sealed share envelope to --out. The connection must present the holder's
certificate. The node keeps no copy of the envelope.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			signer, err := readSigner(keyFile)
			if err != nil {
				return err
			}
			begin, err := target.claimBegin(cmd.Context(), username)
			if err != nil {
				return err
			}
			sig, err := signHexNonce(signer, begin.NonceHex)
			if err != nil {
				return err
			}
			resp, err := target.claimComplete(cmd.Context(), username, begin.NonceHex, sig)
			if err != nil {
				return err
			}
			return writeEnvelope(out, resp)
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&username, "username", "", "Holder username")
	cmd.Flags().StringVar(&keyFile, "key", "", "PEM private key of the holder's certificate")
	cmd.Flags().StringVar(&out, "out", "", "File to write the sealed envelope to")
	for _, name := range []string{"username", "key", "out"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// newHolderClaimBeginCmd returns `claim-begin`, for holders whose key signs
// outside this CLI.
func newHolderClaimBeginCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	var username string
	cmd := &cobra.Command{
		Use:   "claim-begin",
		Short: "Issue the claim nonce (for keys that sign outside this CLI)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			resp, err := target.claimBegin(cmd.Context(), username)
			if err != nil {
				return err
			}
			return renderHolder(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "NonceHex:   %s\n", resp.NonceHex)
				_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:  %s\n", resp.ExpiresAt.Format("2006-01-02T15:04:05Z"))
			})
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&username, "username", "", "Holder username")
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

// newHolderClaimCompleteCmd returns `claim-complete`.
func newHolderClaimCompleteCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	var username, nonceHex, sigFile, out string
	cmd := &cobra.Command{
		Use:   "claim-complete",
		Short: "Complete the claim with an externally produced signature",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			sig, err := os.ReadFile(sigFile)
			if err != nil {
				return &HolderFileError{Path: sigFile, Err: err}
			}
			resp, err := target.claimComplete(cmd.Context(), username, nonceHex, sig)
			if err != nil {
				return err
			}
			return writeEnvelope(out, resp)
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&username, "username", "", "Holder username")
	cmd.Flags().StringVar(&nonceHex, "nonce-hex", "", "Nonce from claim-begin")
	cmd.Flags().StringVar(&sigFile, "sig-file", "", "Raw signature over the nonce bytes")
	cmd.Flags().StringVar(&out, "out", "", "File to write the sealed envelope to")
	for _, name := range []string{"username", "nonce-hex", "sig-file", "out"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// newHolderRevokeCmd returns `revoke`.
func newHolderRevokeCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	var username, soPin string
	var threshold int
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a holder through a quorum-gated rekey",
		Long: `Open a rekey round that drops the holder atomically. It completes when a
quorum of the REMAINING holders submits to the rekey op; the key is then
redistributed to them at --threshold (the current threshold when zero). It is
refused when the threshold would exceed the number of remaining holders.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			round, err := target.revoke(cmd.Context(), username, threshold, soPin)
			if err != nil {
				return err
			}
			return renderQuorumRound(round)
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&username, "username", "", "Holder to revoke")
	cmd.Flags().IntVar(&threshold, "threshold", 0, "Threshold of the remaining share set (0 keeps the current one)")
	if kind.soPinGated() {
		cmd.Flags().StringVar(&soPin, "so-pin", "", "Security Officer PIN")
	}
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

// newHolderRekeyCmd returns the rekey verb.
func newHolderRekeyCmd(use string, kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	var policy, soPin string
	var threshold int
	cmd := &cobra.Command{
		Use:   use,
		Short: "Open a quorum-gated rekey round",
		Long: `Open a rekey round. It completes when a fresh M-of-N quorum of the current
holders submits to the rekey op ('quorum submit --op rekey'); the key is then
redistributed to every holder then enrolled at --threshold under --policy.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			round, err := target.rekey(cmd.Context(), threshold, policy, soPin)
			if err != nil {
				return err
			}
			return renderQuorumRound(round)
		},
	}
	scope.bind(cmd)
	cmd.Flags().IntVar(&threshold, "threshold", 0, "Threshold of the new share set")
	cmd.Flags().StringVar(&policy, "policy", "", "Factor policy: designated (default) or any-enrolled")
	if kind.soPinGated() {
		cmd.Flags().StringVar(&soPin, "so-pin", "", "Security Officer PIN")
	}
	_ = cmd.MarkFlagRequired("threshold")
	return cmd
}

// newHolderQuorumCmd builds the quorum group for a scope.
func newHolderQuorumCmd(use string, kind holderKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: "Authenticated share-holder quorum rounds",
		Long: `A holder answers a factor challenge and submits the share it recovered from
its envelope. The node counts each holder once per round; when M holders have
submitted, the round's operation runs: unseal, seal, root-token, rekey or
recover (the recovery domain).`,
	}
	cmd.AddCommand(newHolderQuorumBeginCmd(kind), newHolderQuorumSubmitCmd(kind))
	return cmd
}

// newHolderQuorumBeginCmd returns `begin`, for factors answered outside this
// CLI (FIDO2, PIV, ...).
func newHolderQuorumBeginCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	var op, username, factor string
	cmd := &cobra.Command{
		Use:   "begin",
		Short: "Issue a quorum challenge",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			challenge, err := target.quorumBegin(cmd.Context(), op, username, factor)
			if err != nil {
				return err
			}
			return renderHolder(challenge, func() {
				_, _ = fmt.Fprintf(outputWriter, "ChallengeID: %s\n", challenge.ChallengeID)
				_, _ = fmt.Fprintf(outputWriter, "Factor:      %s\n", challenge.Factor)
				_, _ = fmt.Fprintf(outputWriter, "NonceHex:    %x\n", challenge.Nonce)
				_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:   %s\n", challenge.ExpiresAt.Format("2006-01-02T15:04:05Z"))
			})
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&op, "op", "", "Operation: unseal, seal, root-token, rekey or recover")
	cmd.Flags().StringVar(&username, "username", "", "Holder username")
	cmd.Flags().StringVar(&factor, "factor", "", "Factor to answer with (the designated factor when empty)")
	_ = cmd.MarkFlagRequired("op")
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

// quorumSubmitFlags are the flags of `quorum submit`.
type quorumSubmitFlags struct {
	op           string
	username     string
	envelope     string
	keyFile      string
	encKeyFile   string
	challengeID  string
	factor       string
	responseFile string
	metadata     string
}

// newHolderQuorumSubmitCmd returns `submit`.
func newHolderQuorumSubmitCmd(kind holderKind) *cobra.Command {
	scope := &holderScope{kind: kind}
	f := &quorumSubmitFlags{}
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a holder's share to the open quorum round",
		Long: `Open the holder's envelope and submit its share, authenticated by a factor.

With --key and no --challenge-id the CLI answers the mtls factor itself: it
asks for a challenge and signs it with the certificate key. For any other
factor, run 'quorum begin', answer the challenge with the factor's own tool,
and pass --challenge-id, --factor and --response-file.

The envelope opens with --encryption-key-file when the holder enrolled a
separate envelope key, else with --key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := scope.target()
			if err != nil {
				return err
			}
			return runQuorumSubmit(cmd.Context(), target, f)
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&f.op, "op", "", "Operation: unseal, seal, root-token, rekey or recover")
	cmd.Flags().StringVar(&f.username, "username", "", "Holder username")
	cmd.Flags().StringVar(&f.envelope, "envelope", "", "The holder's claimed envelope file")
	cmd.Flags().StringVar(&f.keyFile, "key", "", "PEM private key of the holder's certificate")
	cmd.Flags().StringVar(&f.encKeyFile, "encryption-key-file", "", "Raw private key of a separately enrolled envelope key")
	cmd.Flags().StringVar(&f.challengeID, "challenge-id", "", "Challenge answered outside this CLI")
	cmd.Flags().StringVar(&f.factor, "factor", "", "Factor that answered --challenge-id")
	cmd.Flags().StringVar(&f.responseFile, "response-file", "", "Raw factor response to --challenge-id")
	cmd.Flags().StringVar(&f.metadata, "response-metadata", "", "JSON object of factor response metadata")
	for _, name := range []string{"op", "username", "envelope"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// holderSubmission is one holder's quorum submission: the envelope, the key
// that opens it, and either the certificate key that answers the mtls factor
// or a factor response produced elsewhere.
type holderSubmission struct {
	Op       string
	Username string
	Envelope []byte
	// CertKey is the holder's certificate key. It answers the mtls factor
	// when ChallengeID is empty, and opens the envelope when EncKey is empty.
	CertKey crypto.Signer
	// EncKey is a separately enrolled envelope key, raw, in the envelope's
	// suite encoding.
	EncKey []byte
	// ChallengeID, Factor, Response and Metadata carry a factor response
	// produced outside this process.
	ChallengeID string
	Factor      string
	Response    []byte
	Metadata    map[string]string
}

// submitHolderShare answers the factor, opens the envelope and submits the
// share. The CLI and the desktop bindings both submit through it.
func submitHolderShare(ctx context.Context, target holderTarget, in *holderSubmission) (*transport.QuorumOutcome, error) {
	req := &transport.QuorumSubmitRequest{Op: in.Op, Username: in.Username}
	if in.ChallengeID != "" {
		if in.Factor == "" || len(in.Response) == 0 {
			return nil, &MissingArgError{Arg: "factor and response with a challenge id"}
		}
		req.ChallengeID, req.Factor, req.Response, req.ResponseMetadata = in.ChallengeID, in.Factor, in.Response, in.Metadata
	} else {
		if in.CertKey == nil {
			return nil, &MissingArgError{Arg: "certificate key (or a challenge id with a factor response)"}
		}
		challenge, err := target.quorumBegin(ctx, in.Op, in.Username, mtls.Name)
		if err != nil {
			return nil, err
		}
		sig, err := mtls.SignNonce(in.CertKey, challenge.Nonce)
		if err != nil {
			return nil, err
		}
		req.ChallengeID, req.Factor, req.Response = challenge.ChallengeID, mtls.Name, sig
	}
	share, err := openEnvelope(in.Envelope, in.EncKey, in.CertKey)
	if err != nil {
		return nil, err
	}
	defer secure.Wipe(share)
	req.Share = share
	return target.quorumSubmit(ctx, req)
}

// runQuorumSubmit loads the submission's files and submits it.
func runQuorumSubmit(ctx context.Context, target holderTarget, f *quorumSubmitFlags) error {
	in := &holderSubmission{Op: f.op, Username: f.username, ChallengeID: f.challengeID, Factor: f.factor}
	envelope, err := os.ReadFile(f.envelope)
	if err != nil {
		return &HolderFileError{Path: f.envelope, Err: err}
	}
	in.Envelope = envelope
	if f.keyFile != "" {
		signer, err := readSigner(f.keyFile)
		if err != nil {
			return err
		}
		in.CertKey = signer
	}
	if f.encKeyFile != "" {
		key, err := os.ReadFile(f.encKeyFile)
		if err != nil {
			return &HolderFileError{Path: f.encKeyFile, Err: err}
		}
		defer secure.Wipe(key)
		in.EncKey = key
	}
	if f.responseFile != "" {
		response, err := os.ReadFile(f.responseFile)
		if err != nil {
			return &HolderFileError{Path: f.responseFile, Err: err}
		}
		in.Response = response
	}
	if f.metadata != "" {
		if err := json.Unmarshal([]byte(f.metadata), &in.Metadata); err != nil {
			return &InvalidArgError{Arg: "--response-metadata", Reason: "not a JSON object of strings"}
		}
	}
	out, err := submitHolderShare(ctx, target, in)
	if err != nil {
		return err
	}
	return renderHolder(out, func() {
		_, _ = fmt.Fprintf(outputWriter, "Op:         %s\n", out.Op)
		_, _ = fmt.Fprintf(outputWriter, "Submitted:  %d/%d\n", out.Submitted, out.Required)
		_, _ = fmt.Fprintf(outputWriter, "Complete:   %v\n", out.Complete)
		if out.RootToken != "" {
			_, _ = fmt.Fprintf(outputWriter, "RootToken:  %s\n", out.RootToken)
		}
		if out.ShareCount > 0 {
			_, _ = fmt.Fprintf(outputWriter, "NewSplit:   %d-of-%d (%d envelopes to claim)\n", out.Threshold, out.TotalShares, out.ShareCount)
		}
	})
}

// renderQuorumRound prints an open round.
func renderQuorumRound(round *transport.QuorumRound) error {
	return renderHolder(round, func() {
		_, _ = fmt.Fprintf(outputWriter, "Round:      %s/%s\n", round.Domain, round.Op)
		_, _ = fmt.Fprintf(outputWriter, "Required:   %d\n", round.Required)
		_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:  %s\n", round.ExpiresAt.Format("2006-01-02T15:04:05Z"))
		if round.NewThreshold > 0 {
			_, _ = fmt.Fprintf(outputWriter, "NewThreshold: %d\n", round.NewThreshold)
		}
		if len(round.Remove) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Removing:   %s\n", strings.Join(round.Remove, ","))
		}
	})
}

// renderHolder prints v as JSON or YAML when requested, else runs table.
func renderHolder(v any, table func()) error {
	switch outputFormat {
	case "json":
		return printJSON(v)
	case "yaml":
		return printYAML(v)
	default:
		table()
		return nil
	}
}

// newTenantBarrierHolderCmds returns the tenant-scoped holder verbs.
func newTenantBarrierHolderCmds() []*cobra.Command {
	return []*cobra.Command{
		newHolderOfficerCmd("barrier-officer", "Manage a tenant barrier's share holders", holderTenant),
		newHolderQuorumCmd("barrier-quorum", holderTenant),
		newHolderRekeyCmd("barrier-rekey", holderTenant),
	}
}

// newDatabaseBarrierHolderCmds returns the database-scoped holder verbs: the
// tenant forms taking --database, plus the custody initialization a tenant
// barrier performs at init.
func newDatabaseBarrierHolderCmds() []*cobra.Command {
	return []*cobra.Command{
		newHolderOfficerCmd("barrier-officer", "Manage a database barrier's share holders", holderDatabase),
		newDatabaseInitShamirCmd(),
		newHolderQuorumCmd("barrier-quorum", holderDatabase),
		newHolderRekeyCmd("barrier-rekey", holderDatabase),
	}
}

// newDatabaseInitShamirCmd returns `barrier-init-shamir`.
func newDatabaseInitShamirCmd() *cobra.Command {
	var database, policy string
	cmd := &cobra.Command{
		Use:   "barrier-init-shamir",
		Short: "Initialize a Shamir database barrier's custody",
		Long: `Split the database's root key to its enrolled holders, at the M-of-N the
database was created with (--barrier-shamir-threshold of --barrier-shamir-total);
exactly that many holders must be enrolled. Each holder then claims its sealed
envelope once with 'barrier-officer claim'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := databaseHolders{cl: client, database: database}.initialize(cmd.Context(), policy)
			if err != nil {
				return err
			}
			return renderHolder(resp, func() {
				_, _ = fmt.Fprintf(outputWriter, "Split:      %d-of-%d (%d envelopes to claim)\n", resp.Threshold, resp.TotalShares, resp.ShareCount)
			})
		},
	}
	cmd.Flags().StringVar(&database, "database", "", "Database whose barrier to initialize")
	cmd.Flags().StringVar(&policy, "policy", "", "Factor policy: designated (default) or any-enrolled")
	_ = cmd.MarkFlagRequired("database")
	return cmd
}
