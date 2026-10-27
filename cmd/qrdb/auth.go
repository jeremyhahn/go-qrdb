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

// auth.go implements the canonical "auth" command group per
// docs/auth-factors/cli.md. The command group exposes
// login and N-factor management (list / add / remove / rotate /
// relabel) wired against the qrdb SDK user-side AuthFactor service
// (M14, accessed via Client.UserAuthFactors()).
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// validFactorTypes is the canonical --type set for "auth factor add"
// per docs/auth-factors/architecture.md.
var validFactorTypes = map[string]struct{}{
	"mtls":  {},
	"fido2": {},
	"piv":   {},
}

// validAuthMethods is the canonical --method set for "auth login" per
// docs/auth-factors/architecture.md.
var validAuthMethods = map[string]struct{}{
	"mtls":  {},
	"fido2": {},
	"piv":   {},
}

// ---------------------------------------------------------------------------
// Typed errors
// ---------------------------------------------------------------------------

// MissingFactorIDError is returned when an "auth factor" subcommand
// requires a positional <factor-id> argument and none was supplied.
type MissingFactorIDError struct {
	Op string
}

// Error returns a human-readable description.
func (e *MissingFactorIDError) Error() string {
	return e.Op + ": factor id is required"
}

// MissingLabelError is returned when --label is required and missing.
type MissingLabelError struct {
	Op string
}

// Error returns a human-readable description.
func (e *MissingLabelError) Error() string {
	return e.Op + ": --label is required"
}

// MissingUserError is returned when --user is required and missing.
type MissingUserError struct {
	Op string
}

// Error returns a human-readable description.
func (e *MissingUserError) Error() string {
	return e.Op + ": --user is required"
}

// InvalidUserIDError is returned when --user cannot be parsed as a
// uint64. The user-side AuthFactor service uses uint64 user identifiers
// matching the upstream user store; CLI users supply the numeric form.
type InvalidUserIDError struct {
	Value string
	Cause error
}

// Error returns a human-readable description.
func (e *InvalidUserIDError) Error() string {
	return "invalid --user " + e.Value + " (must be a numeric user id): " + e.Cause.Error()
}

// Unwrap returns the wrapped cause for errors.Is/As.
func (e *InvalidUserIDError) Unwrap() error { return e.Cause }

// InvalidFactorTypeError is returned when --type is not one of the
// canonical factor types.
type InvalidFactorTypeError struct {
	Type string
}

// Error returns a human-readable description.
func (e *InvalidFactorTypeError) Error() string {
	return "invalid --type " + e.Type + " (must be one of: mtls, fido2, piv)"
}

// InvalidAuthMethodError is returned when --method is not one of the
// canonical auth methods.
type InvalidAuthMethodError struct {
	Method string
}

// Error returns a human-readable description.
func (e *InvalidAuthMethodError) Error() string {
	return "invalid --method " + e.Method + " (must be one of: mtls, fido2, piv)"
}

// MissingCertPathError is returned when "--type mtls" is selected but
// --cert was not supplied.
type MissingCertPathError struct{}

// Error returns a human-readable description.
func (e *MissingCertPathError) Error() string {
	return "auth factor add --type mtls: --cert <path> is required"
}

// MissingCredentialError is returned when --type fido2|piv is selected
// for an operation that requires the credential bytes (Add/Rotate) and
// no source for them was supplied (--credential-hex / --cert).
type MissingCredentialError struct {
	Type string
}

// Error returns a human-readable description.
func (e *MissingCredentialError) Error() string {
	return "auth factor add --type " + e.Type + ": --credential-hex <hex> is required (FIDO2 credentialID / PIV slot cert hash)"
}

// CertReadError wraps errors raised while reading or parsing a cert
// PEM file.
type CertReadError struct {
	Path  string
	Cause error
}

// Error returns a human-readable description.
func (e *CertReadError) Error() string {
	if e.Path == "" {
		return "cert read error: " + e.Cause.Error()
	}
	return "cert read error (" + e.Path + "): " + e.Cause.Error()
}

// Unwrap returns the wrapped cause for errors.Is/As.
func (e *CertReadError) Unwrap() error { return e.Cause }

// InvalidCredentialHexError is returned when --credential-hex cannot be
// decoded as a hex string.
type InvalidCredentialHexError struct {
	Cause error
}

// Error returns a human-readable description.
func (e *InvalidCredentialHexError) Error() string {
	return "invalid --credential-hex: " + e.Cause.Error()
}

// Unwrap returns the wrapped cause for errors.Is/As.
func (e *InvalidCredentialHexError) Unwrap() error { return e.Cause }

// AuthFactorTransportUnsupportedError is returned when the active
// transport does not implement the user-side AuthFactor surface (M14).
// Embedded transports only expose the legacy ceremony AuthFactorClient.
type AuthFactorTransportUnsupportedError struct{}

// Error returns a human-readable description.
func (e *AuthFactorTransportUnsupportedError) Error() string {
	return "auth factor: active transport does not implement the user-side AuthFactor service (M14); use a network transport (rest, grpc, quic, mcp, unix)"
}

// ---------------------------------------------------------------------------
// Command tree
// ---------------------------------------------------------------------------

// newAuthCmd builds the canonical "auth" command group. The factor
// subcommands wire to the qrdb SDK user-side AuthFactor surface
// (`client.UserAuthFactors()`).
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "User authentication and N-factor management",
		Long: `User authentication operations.

  login                 Authenticate the active session
  factor list           List enrolled factors for a user
  factor add            Enroll a new factor (mTLS, FIDO2, PIV)
  factor remove         Remove an enrolled factor by id
  factor rotate         Rotate an enrolled factor's key material
  factor relabel        Relabel an enrolled factor

The factor subcommands implement the N-factor model documented in
docs/auth-factors/architecture.md and delegate to the qrdb
SDK user-side AuthFactor service (M14).`,
	}

	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthFactorCmd())
	return cmd
}

// newAuthLoginCmd implements "auth login [--method mtls|fido2|piv]
// [--cert <path>] [--credential-hex <hex>]".
func newAuthLoginCmd() *cobra.Command {
	var (
		method   string
		certPath string
		credHex  string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate the active session",
		Long: `Authenticate using one of the enrolled factors.

The --method flag selects the auth method (mtls, fido2, piv). The
--cert / --credential-hex flag supplies the lookup key:

  mtls : --cert <path>           SHA-256 of the certificate DER
  fido2: --credential-hex <hex>  WebAuthn credentialID bytes
  piv  : --credential-hex <hex>  SHA-256 of the slot certificate DER

The qrdb CLI does not perform hardware verification; it resolves the
(method, lookup key) tuple to a user via the M14 LoginAuthFactor RPC.
For end-to-end hardware login use 'xkmsctl auth login' or
'platformctl auth login'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthLogin(cmd.Context(), method, certPath, credHex)
		},
	}
	cmd.Flags().StringVar(&method, "method", "", "Auth method: mtls | fido2 | piv (required)")
	cmd.Flags().StringVar(&certPath, "cert", "", "Certificate PEM path (--method mtls)")
	cmd.Flags().StringVar(&credHex, "credential-hex", "", "Credential bytes as hex (--method fido2|piv)")
	return cmd
}

// newAuthFactorCmd builds the "auth factor" subcommand group.
func newAuthFactorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "factor",
		Short: "N-factor management (list, add, remove, rotate, relabel)",
	}
	cmd.AddCommand(newAuthFactorListCmd())
	cmd.AddCommand(newAuthFactorAddCmd())
	cmd.AddCommand(newAuthFactorRemoveCmd())
	cmd.AddCommand(newAuthFactorDeleteCmd())
	cmd.AddCommand(newAuthFactorRotateCmd())
	cmd.AddCommand(newAuthFactorRelabelCmd())
	return cmd
}

// newAuthFactorListCmd implements "auth factor list --user <id>".
func newAuthFactorListCmd() *cobra.Command {
	var user string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List enrolled factors for a user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthFactorList(cmd.Context(), user)
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "Numeric user id (required)")
	return cmd
}

// newAuthFactorAddCmd implements "auth factor add --user <id> --type
// <t> --label <name> [type-specific flags]".
func newAuthFactorAddCmd() *cobra.Command {
	flags := authFactorAddFlags{}
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Enroll a new factor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags.hasExpectedVersion = cmd.Flags().Changed("expected-version")
			return runAuthFactorAdd(cmd.Context(), &flags)
		},
	}
	bindAuthFactorAddFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.ifNoActiveType, "if-no-active-type", false,
		"Add only if the user has no active factor of --type")
	cmd.Flags().Uint64Var(&flags.expectedVersion, "expected-version", 0,
		"Add only if the user's factor set is at this version (see 'auth factor list')")
	return cmd
}

// authFactorAddFlags consolidates the type-agnostic + type-specific
// "auth factor add" flag set.
type authFactorAddFlags struct {
	factorType    string
	label         string
	user          string
	createdBy     string
	certPath      string
	credentialHex string
	fido2Device   string
	pivReader     string
	pivSlot       string

	// ifNoActiveType, expectedVersion and hasExpectedVersion are the add
	// condition. They are bound on add only: a rotation refuses a condition.
	ifNoActiveType     bool
	expectedVersion    uint64
	hasExpectedVersion bool
}

// bindAuthFactorAddFlags wires the canonical add/rotate flag set onto a
// cobra command. Reused by both add and rotate.
func bindAuthFactorAddFlags(cmd *cobra.Command, f *authFactorAddFlags) {
	cmd.Flags().StringVar(&f.factorType, "type", "", "Factor type: mtls | fido2 | piv (required)")
	cmd.Flags().StringVar(&f.label, "label", "", "Human-readable factor label (required)")
	cmd.Flags().StringVar(&f.user, "user", "", "Numeric user id (required for add; inherited for rotate)")
	cmd.Flags().StringVar(&f.createdBy, "created-by", "self", "Provenance string: self | admin | ceremony")
	cmd.Flags().StringVar(&f.certPath, "cert", "", "Certificate PEM path (--type mtls)")
	cmd.Flags().StringVar(&f.credentialHex, "credential-hex", "", "Credential bytes as hex (--type fido2|piv)")
	cmd.Flags().StringVar(&f.fido2Device, "device", "", "FIDO2 device id metadata (--type fido2)")
	cmd.Flags().StringVar(&f.pivReader, "reader", "", "PC/SC reader name metadata (--type piv)")
	cmd.Flags().StringVar(&f.pivSlot, "slot", "9a", "PIV slot id metadata (--type piv; default 9a)")
}

// newAuthFactorRemoveCmd implements "auth factor remove <factor-id>
// [--force]".
func newAuthFactorRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <factor-id>",
		Short: "Remove an enrolled factor",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &MissingFactorIDError{Op: "auth factor remove"}
			}
			return runAuthFactorRemove(cmd.Context(), args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Allow removing the last factor (admin)")
	return cmd
}

// newAuthFactorDeleteCmd implements "auth factor delete <factor-id>
// [--force]": the hard delete that rolls back an add, freeing the
// credential to be bound again. A revoked factor is refused.
func newAuthFactorDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <factor-id>",
		Short: "Delete a factor outright so its credential can be bound again",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &MissingFactorIDError{Op: "auth factor delete"}
			}
			return runAuthFactorDelete(cmd.Context(), args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Allow deleting the last factor (admin)")
	return cmd
}

// newAuthFactorRotateCmd implements "auth factor rotate <factor-id>
// --type <t> --label <name> ...".
func newAuthFactorRotateCmd() *cobra.Command {
	flags := authFactorAddFlags{}
	cmd := &cobra.Command{
		Use:   "rotate <factor-id>",
		Short: "Rotate an enrolled factor",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &MissingFactorIDError{Op: "auth factor rotate"}
			}
			return runAuthFactorRotate(cmd.Context(), args[0], &flags)
		},
	}
	bindAuthFactorAddFlags(cmd, &flags)
	return cmd
}

// newAuthFactorRelabelCmd implements "auth factor relabel <factor-id>
// --label <name>". Wired to the M14 RelabelAuthFactor RPC.
func newAuthFactorRelabelCmd() *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "relabel <factor-id>",
		Short: "Relabel an enrolled factor",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return &MissingFactorIDError{Op: "auth factor relabel"}
			}
			if label == "" {
				return &MissingLabelError{Op: "auth factor relabel"}
			}
			return runAuthFactorRelabel(cmd.Context(), args[0], label)
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "New human-readable label (required)")
	return cmd
}

// ---------------------------------------------------------------------------
// Client resolution
// ---------------------------------------------------------------------------

// userAuthFactorClient resolves the user-side AuthFactor client from
// the active SDK client. Returns AuthFactorTransportUnsupportedError if
// the active transport does not satisfy transport.UserAuthFactorClient.
func userAuthFactorClient() (transport.UserAuthFactorClient, error) {
	if client == nil {
		return nil, &AuthFactorTransportUnsupportedError{}
	}
	uac := client.UserAuthFactors()
	if uac == nil {
		return nil, &AuthFactorTransportUnsupportedError{}
	}
	return uac, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// runAuthFactorList dispatches "auth factor list".
func runAuthFactorList(ctx context.Context, user string) error {
	if user == "" {
		return &MissingUserError{Op: "auth factor list"}
	}
	userID, err := parseUserID(user)
	if err != nil {
		return err
	}
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	resp, err := cl.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: userID})
	if err != nil {
		return err
	}
	return printFactorList(resp)
}

// printFactorList renders an AuthFactorListResponse.
func printFactorList(resp *transport.AuthFactorListResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		headers := []string{"ID", "TYPE", "USER", "LABEL", "REGISTERED_AT", "REVOKED_AT"}
		rows := make([][]string, 0, len(resp.Factors))
		for _, f := range resp.Factors {
			revoked := "-"
			if f.RevokedAt != nil {
				revoked = f.RevokedAt.Format("2006-01-02T15:04:05Z")
			}
			rows = append(rows, []string{
				f.ID,
				f.Type,
				strconv.FormatUint(f.UserID, 10),
				f.Label,
				f.RegisteredAt.Format("2006-01-02T15:04:05Z"),
				revoked,
			})
		}
		printTable(headers, rows)
		_, _ = fmt.Fprintf(outputWriter, "Factor set version: %d\n", resp.FactorSetVersion)
	}
	return nil
}

// runAuthFactorAdd dispatches "auth factor add".
func runAuthFactorAdd(ctx context.Context, f *authFactorAddFlags) error {
	if f.user == "" {
		return &MissingUserError{Op: "auth factor add"}
	}
	userID, err := parseUserID(f.user)
	if err != nil {
		return err
	}
	req, err := buildAddRequest(userID, f)
	if err != nil {
		return err
	}
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	resp, err := cl.AddAuthFactor(ctx, req)
	if err != nil {
		return err
	}
	return printFactorAdd(resp)
}

// factorAddOutput is the record an add prints, with the factor-set version
// a conditional add committed beside the record's own fields, so the
// record's shape is the same whether or not the add carried a condition.
type factorAddOutput struct {
	transport.AuthFactorRecord `yaml:",inline"`
	FactorSetVersion           uint64 `json:"factor_set_version,omitempty" yaml:",omitempty"`
}

// printFactorAdd renders an AuthFactorAddResponse.
func printFactorAdd(resp *transport.AuthFactorAddResponse) error {
	out := factorAddOutput{AuthFactorRecord: resp.Factor, FactorSetVersion: resp.FactorSetVersion}
	switch outputFormat {
	case "json":
		return printJSON(out)
	case "yaml":
		return printYAML(out)
	default:
		printFactorRecordText(&resp.Factor)
		if resp.FactorSetVersion != 0 {
			_, _ = fmt.Fprintf(outputWriter, "Factor Set:    version %d\n", resp.FactorSetVersion)
		}
	}
	return nil
}

// printFactorRecord renders a single AuthFactorRecord.
func printFactorRecord(rec *transport.AuthFactorRecord) error {
	switch outputFormat {
	case "json":
		return printJSON(rec)
	case "yaml":
		return printYAML(rec)
	default:
		printFactorRecordText(rec)
	}
	return nil
}

// printFactorRecordText renders rec as the text output's labelled lines.
func printFactorRecordText(rec *transport.AuthFactorRecord) {
	_, _ = fmt.Fprintf(outputWriter, "ID:            %s\n", rec.ID)
	_, _ = fmt.Fprintf(outputWriter, "Type:          %s\n", rec.Type)
	_, _ = fmt.Fprintf(outputWriter, "User:          %d\n", rec.UserID)
	if rec.Label != "" {
		_, _ = fmt.Fprintf(outputWriter, "Label:         %s\n", rec.Label)
	}
	_, _ = fmt.Fprintf(outputWriter, "Registered At: %s\n", rec.RegisteredAt.Format("2006-01-02T15:04:05Z"))
	if rec.RevokedAt != nil {
		_, _ = fmt.Fprintf(outputWriter, "Revoked At:    %s\n", rec.RevokedAt.Format("2006-01-02T15:04:05Z"))
	}
	if rec.CreatedBy != "" {
		_, _ = fmt.Fprintf(outputWriter, "Created By:    %s\n", rec.CreatedBy)
	}
}

// buildAddRequest validates the canonical add/rotate flag set and
// builds an AuthFactorAddRequest with type-specific Credential and
// Metadata.
func buildAddRequest(userID uint64, f *authFactorAddFlags) (*transport.AuthFactorAddRequest, error) {
	if f.factorType == "" {
		return nil, &InvalidFactorTypeError{Type: ""}
	}
	if _, ok := validFactorTypes[f.factorType]; !ok {
		return nil, &InvalidFactorTypeError{Type: f.factorType}
	}
	if f.label == "" {
		return nil, &MissingLabelError{Op: "auth factor add"}
	}

	credential, metadata, err := credentialAndMetadata(f)
	if err != nil {
		return nil, err
	}

	req := &transport.AuthFactorAddRequest{
		UserID:         userID,
		Type:           f.factorType,
		Label:          f.label,
		Credential:     credential,
		Metadata:       metadata,
		CreatedBy:      f.createdBy,
		IfNoActiveType: f.ifNoActiveType,
	}
	if f.hasExpectedVersion {
		version := f.expectedVersion
		req.ExpectedFactorSetVersion = &version
	}
	return req, nil
}

// credentialAndMetadata derives the canonical credential lookup-key
// bytes and the type-specific metadata map from the add/rotate flags.
func credentialAndMetadata(f *authFactorAddFlags) ([]byte, map[string]string, error) {
	metadata := map[string]string{}
	switch f.factorType {
	case "mtls":
		if f.certPath == "" {
			return nil, nil, &MissingCertPathError{}
		}
		credBytes, certMeta, err := loadCertCredentialAndMetadata(f.certPath)
		if err != nil {
			return nil, nil, err
		}
		for k, v := range certMeta {
			metadata[k] = v
		}
		return credBytes, metadata, nil
	case "fido2":
		if f.credentialHex == "" {
			return nil, nil, &MissingCredentialError{Type: "fido2"}
		}
		credBytes, err := hex.DecodeString(f.credentialHex)
		if err != nil {
			return nil, nil, &InvalidCredentialHexError{Cause: err}
		}
		if f.fido2Device != "" {
			metadata["device_id"] = f.fido2Device
		}
		return credBytes, metadata, nil
	case "piv":
		if f.credentialHex == "" {
			return nil, nil, &MissingCredentialError{Type: "piv"}
		}
		credBytes, err := hex.DecodeString(f.credentialHex)
		if err != nil {
			return nil, nil, &InvalidCredentialHexError{Cause: err}
		}
		if f.pivReader != "" {
			metadata["reader"] = f.pivReader
		}
		if f.pivSlot != "" {
			metadata["slot"] = f.pivSlot
		}
		return credBytes, metadata, nil
	}
	return nil, nil, &InvalidFactorTypeError{Type: f.factorType}
}

// loadCertCredentialAndMetadata reads a PEM certificate from path,
// returns the SHA-256 of the DER bytes (the canonical mTLS lookup key)
// plus selected metadata (CN / serial / not-after).
func loadCertCredentialAndMetadata(path string) ([]byte, map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, &CertReadError{Path: path, Cause: err}
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, &CertReadError{Path: path, Cause: errors.New("no CERTIFICATE block found")}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, &CertReadError{Path: path, Cause: err}
	}
	sum := sha256.Sum256(cert.Raw)
	meta := map[string]string{
		"cert_cn":          cert.Subject.CommonName,
		"cert_serial":      cert.SerialNumber.String(),
		"cert_not_after":   cert.NotAfter.UTC().Format("2006-01-02T15:04:05Z"),
		"cert_not_before":  cert.NotBefore.UTC().Format("2006-01-02T15:04:05Z"),
		"cert_fingerprint": hex.EncodeToString(sum[:]),
	}
	return sum[:], meta, nil
}

// runAuthFactorRemove dispatches "auth factor remove".
func runAuthFactorRemove(ctx context.Context, factorID string, force bool) error {
	if factorID == "" {
		return &MissingFactorIDError{Op: "auth factor remove"}
	}
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	if _, err := cl.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{
		FactorID: factorID,
		Force:    force,
	}); err != nil {
		return err
	}
	printOK()
	return nil
}

// runAuthFactorDelete dispatches "auth factor delete".
func runAuthFactorDelete(ctx context.Context, factorID string, force bool) error {
	if factorID == "" {
		return &MissingFactorIDError{Op: "auth factor delete"}
	}
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	if _, err := cl.DeleteAuthFactor(ctx, &transport.AuthFactorDeleteRequest{
		FactorID: factorID,
		Force:    force,
	}); err != nil {
		return err
	}
	printOK()
	return nil
}

// runAuthFactorRotate dispatches "auth factor rotate" via the atomic
// M14 RotateAuthFactor RPC.
func runAuthFactorRotate(ctx context.Context, oldID string, f *authFactorAddFlags) error {
	if oldID == "" {
		return &MissingFactorIDError{Op: "auth factor rotate"}
	}
	// User id is optional for rotate: the server inherits it from the
	// existing factor when zero.
	var userID uint64
	if f.user != "" {
		parsed, err := parseUserID(f.user)
		if err != nil {
			return err
		}
		userID = parsed
	}
	addReq, err := buildAddRequest(userID, f)
	if err != nil {
		return err
	}
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	resp, err := cl.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{
		OldFactorID: oldID,
		NewFactor:   *addReq,
	})
	if err != nil {
		return err
	}

	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Old factor revoked: %s\n", oldID)
		return printFactorRecord(&resp.NewFactor)
	}
}

// runAuthFactorRelabel dispatches "auth factor relabel" via the M14
// RelabelAuthFactor RPC.
func runAuthFactorRelabel(ctx context.Context, factorID, label string) error {
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	resp, err := cl.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{
		FactorID: factorID,
		Label:    label,
	})
	if err != nil {
		return err
	}
	return printFactorRecord(&resp.Factor)
}

// runAuthLogin dispatches "auth login" via the M14 LoginAuthFactor
// RPC. The qrdb CLI does not perform hardware verification — it
// resolves a (Type, LookupKey) tuple to a user record. mTLS lookups
// derive the lookup key from a cert PEM via SHA-256(DER); fido2/piv
// lookups require --credential-hex.
func runAuthLogin(ctx context.Context, method, certPath, credHex string) error {
	if method == "" {
		return &InvalidAuthMethodError{Method: ""}
	}
	if _, ok := validAuthMethods[method]; !ok {
		return &InvalidAuthMethodError{Method: method}
	}
	lookupKey, err := buildLoginLookupKey(method, certPath, credHex)
	if err != nil {
		return err
	}
	cl, err := userAuthFactorClient()
	if err != nil {
		return err
	}
	resp, err := cl.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{
		Type:      method,
		LookupKey: lookupKey,
	})
	if err != nil {
		return err
	}

	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "User ID:   %d\n", resp.UserID)
		_, _ = fmt.Fprintf(outputWriter, "Username:  %s\n", resp.Username)
		if resp.Email != "" {
			_, _ = fmt.Fprintf(outputWriter, "Email:     %s\n", resp.Email)
		}
		if resp.TenantID != "" {
			_, _ = fmt.Fprintf(outputWriter, "Tenant:    %s\n", resp.TenantID)
		}
		_, _ = fmt.Fprintf(outputWriter, "Active:    %t\n", resp.Active)
		if len(resp.Roles) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Roles:     %v\n", resp.Roles)
		}
		_, _ = fmt.Fprintf(outputWriter, "Factor ID: %s\n", resp.Factor.ID)
		_, _ = fmt.Fprintf(outputWriter, "Factor:    %s (%s)\n", resp.Factor.Label, resp.Factor.Type)
	}
	return nil
}

// buildLoginLookupKey derives the canonical lookup key bytes for an
// auth method.
func buildLoginLookupKey(method, certPath, credHex string) ([]byte, error) {
	switch method {
	case "mtls":
		if certPath == "" {
			return nil, &MissingCertPathError{}
		}
		key, _, err := loadCertCredentialAndMetadata(certPath)
		return key, err
	case "fido2", "piv":
		if credHex == "" {
			return nil, &MissingCredentialError{Type: method}
		}
		key, err := hex.DecodeString(credHex)
		if err != nil {
			return nil, &InvalidCredentialHexError{Cause: err}
		}
		return key, nil
	}
	return nil, &InvalidAuthMethodError{Method: method}
}

// parseUserID parses a numeric user identifier string.
func parseUserID(s string) (uint64, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, &InvalidUserIDError{Value: s, Cause: err}
	}
	return id, nil
}
