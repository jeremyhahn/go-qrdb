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

// init.go implements the canonical root-level "init" command per
// docs/ceremony/cli.md. The command bootstraps the
// barrier, the CA and the built-in roles in a single call, delegating to the
// qrdb CeremonyService over the SDK transport. The bootstrap admin's
// certificate is resolved in that SAME call (CSR-sign, bring-your-own, or
// generate -- see --admin-csr/--admin-cert/docs/firstboot/user.md) and
// printed here; BINDING it to the admin account happens slightly later, once
// first boot has created that account (go-qrdb#548).
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// validBarrierStrategies is the set of supported --barrier-strategy values
// per docs/barrier-seal/architecture.md.
var validBarrierStrategies = map[string]struct{}{
	"none":       {},
	"passphrase": {},
	"shamir":     {},
	"pkcs11":     {},
	"tpm2":       {},
	"awskms":     {},
	"gcpkms":     {},
	"azurekv":    {},
	"vault":      {},
}

// barrierStrategiesNeedingPassphrase are the --barrier-strategy values
// qrdb init's --passphrase is required for: a single, operator-supplied
// secret the system barrier is brought up with. "none" rejects it
// outright. "shamir" here always means M-of-N officer enrollment (CLI
// validation below requires --threshold >= 2 whenever --barrier-strategy
// is "shamir"), which ignores --passphrase regardless: the system barrier
// stays sealed until officers reconstruct it from their shares, exactly
// as before this command brought up the barrier itself. This "shamir" is
// unrelated to `barrier init --barrier-strategy shamir`'s Shamir-SPLIT-
// SEED meaning (a single secret, derived by splitting a Shamir seed, as
// opposed to M-of-N officer custody) -- a DIFFERENT barrier-bringup path
// this command does not use, and which is now ALSO refused regardless of
// which --barrier-strategy value accompanies it: `barrier init` refuses
// the system barrier unconditionally, not strategy by strategy.
var barrierStrategiesNeedingPassphrase = map[string]struct{}{
	"passphrase": {},
}

// initFlags holds the canonical init flag set shared by all four binaries.
type initFlags struct {
	soPin           string
	userPin         string
	barrierStrategy string
	passphrase      string
	shares          int
	threshold       int
	hostname        string
	sans            string
	policy          string
	officers        []string
	custodians      []string
	officersFile    string
	adminCSR        string
	adminCert       string
	adminCertPool   string
	adminGenerate   bool
}

// newInitCmd returns the canonical root-level "init" command.
//
// It is the ONLY system bootstrap (go-qrdb#548): in one call, against the
// sealed-window listener before anything is unsealed, it initializes the
// system barrier, runs the ceremony, and issues the bootstrap admin's
// credential. `barrier init` has only ever been the system barrier (its
// REST endpoint carries no tenant or database scope) and now refuses
// unconditionally, pointing here instead; `tenant barrier-init` is the
// separate, untouched command a tenant admin uses for their own barrier.
// Per CLI Surface Convergence (§6) the same command, the same flag set and
// the same env-var bindings appear on every binary.
func newInitCmd() *cobra.Command {
	f := &initFlags{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the system barrier, CA, admin cert and roles in one call",
		Long: `Bootstrap a fresh qrdb system in one call.

The init command delegates to the qrdb CeremonyService and brings up the
system barrier, the CA, the initial admin certificate and the default role
set in a single transactional ceremony, running entirely against the
sealed-window listener before anything else is unsealed. --passphrase is
required when --barrier-strategy needs one (e.g. "passphrase") and
rejected when --barrier-strategy is "none". A fresh node's system barrier
cannot be brought up any other way: ` + "`barrier init`" + ` refuses it and
names this command instead.

Canonical environment variables (Viper-bound):
  QRDB_SO_PIN              equivalent to --so-pin
  QRDB_USER_PIN            equivalent to --user-pin
  QRDB_BARRIER_STRATEGY    equivalent to --barrier-strategy
  QRDB_PASSPHRASE          equivalent to --passphrase
  QRDB_SHARES              equivalent to --shares
  QRDB_THRESHOLD           equivalent to --threshold
  QRDB_HOSTNAME            equivalent to --hostname
  QRDB_SANS                equivalent to --sans

M-of-N Shamir custody (--barrier-strategy shamir with --threshold >= 2)
names the initial share holders up front: Security Officers via --officers
and key custodians via --custodians (both repeatable), each entry
username=path/to/csr.pem, where the CSR supplies only the holder's public
key — the node's CA assigns the certificate identity. Officers and
custodians are enrolled the same way and together make up --shares. A
Threshold>=2 request with no holders fails closed with the ceremony's
typed ErrNoOfficers. In that mode the system barrier is NOT brought up by
this call: it stays sealed until holders later reconstruct it from their
shares. Each holder then claims its sealed share envelope with 'barrier
officer claim'.

--officers/--custodians can only name a holder's username and CSR. A
holder that also needs a separate share-envelope key, an additional
authentication factor, or a designated factor uses --officers-file
instead: a JSON document naming every holder with the full shape (see
docs/ceremony/cli.md), mutually exclusive with --officers/--custodians.

The bootstrap admin's certificate is resolved in this same call through
exactly one of --admin-csr (CSR-sign, the admin's key never leaves this
machine), --admin-cert (bring-your-own-certificate, nothing is issued), or
--admin-generate (dev/test/evaluation ONLY; the server issues a key pair
and returns the private key below exactly once). Exactly one is required;
there is no credential-less default.

Examples:
  qrdb init --barrier-strategy none --so-pin SOPIN --user-pin USERPIN
  qrdb init --barrier-strategy passphrase --passphrase '<passphrase>' \
            --so-pin SOPIN --user-pin USERPIN
  qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
            --so-pin SOPIN --user-pin USERPIN \
            --officers alice=alice.csr --officers bob=bob.csr \
            --custodians carol=carol.csr --custodians dave=dave.csr \
            --custodians erin=erin.csr
  qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
            --so-pin SOPIN --user-pin USERPIN \
            --officers-file officers.json
  qrdb init --barrier-strategy none --so-pin SOPIN --user-pin USERPIN \
            --admin-csr admin.csr
  QRDB_SO_PIN=... QRDB_USER_PIN=... qrdb init --barrier-strategy none --admin-generate`,
		Args: cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return resolveAndValidateInitFlags(cmd, f, "QRDB")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, f)
		},
	}

	registerInitFlags(cmd, f)
	return cmd
}

// registerInitFlags attaches the canonical init flag set to a cobra
// command. The flag set is identical across qrdb, xkmsd, quicbaasd and
// platformctl.
func registerInitFlags(cmd *cobra.Command, f *initFlags) {
	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (required, FIPS 140-3 / PCI-DSS v4 SO credential)")
	cmd.Flags().StringVar(&f.userPin, "user-pin", "",
		"User PIN (required, FIPS 140-3 / PCI-DSS v4 user credential)")
	cmd.Flags().StringVar(&f.barrierStrategy, "barrier-strategy", "passphrase",
		"Barrier sealing strategy: passphrase | shamir | pkcs11 | tpm2 | awskms | gcpkms | azurekv | vault")
	cmd.Flags().StringVar(&f.passphrase, "passphrase", "",
		"System barrier passphrase (required when --barrier-strategy needs one, e.g. passphrase; rejected when --barrier-strategy=none). Not used in M-of-N mode (--threshold >= 2 with --officers), where the barrier stays sealed until officers reconstruct it.")
	cmd.Flags().IntVar(&f.shares, "shares", 0,
		"Total Shamir shares (required when --barrier-strategy=shamir)")
	cmd.Flags().IntVar(&f.threshold, "threshold", 0,
		"Shamir M-of-N threshold (required when --barrier-strategy=shamir)")
	cmd.Flags().StringVar(&f.hostname, "hostname", "",
		"FQDN written into the admin/server certificate CN")
	cmd.Flags().StringVar(&f.sans, "sans", "",
		"Comma-separated additional DNS / IP SANs")
	cmd.Flags().StringArrayVar(&f.officers, "officers", nil,
		"Security Officer share holder: username=path/to/csr.pem (repeatable; required when --barrier-strategy=shamir with --threshold >= 2). Mutually exclusive with --officers-file.")
	cmd.Flags().StringArrayVar(&f.custodians, "custodians", nil,
		"Key custodian share holder: username=path/to/csr.pem (repeatable). Mutually exclusive with --officers-file.")
	cmd.Flags().StringVar(&f.officersFile, "officers-file", "",
		"Path to a JSON file naming every initial share holder with the full holder shape (separate envelope key, extra factors, designated factor) that --officers/--custodians cannot express; see docs/ceremony/cli.md. Mutually exclusive with --officers/--custodians.")
	cmd.Flags().StringVar(&f.policy, "policy", "",
		"Factor policy of the initial share set: designated (default) or any-enrolled")
	cmd.Flags().StringVar(&f.adminCSR, "admin-csr", "",
		"Path to a PEM-encoded CSR for the bootstrap admin's certificate (CSR-sign path; the admin's key never leaves this machine). Mutually exclusive with --admin-cert.")
	cmd.Flags().StringVar(&f.adminCert, "admin-cert", "",
		"Path to a PEM-encoded certificate the admin already holds (bring-your-own-certificate path; nothing is issued). Mutually exclusive with --admin-csr. Requires --admin-cert-pool unless the certificate was issued by this deployment's own CA.")
	cmd.Flags().StringVar(&f.adminCertPool, "admin-cert-pool", "",
		"Path to a PEM-encoded trust anchor bundle --admin-cert must chain to. Only used with --admin-cert.")
	cmd.Flags().BoolVar(&f.adminGenerate, "admin-generate", false,
		"Generate the bootstrap admin's key pair and certificate in-process, returning the private key exactly once. Mutually exclusive with --admin-csr/--admin-cert. NOT suitable for production (dev/test/evaluation ONLY). Exactly one of --admin-csr, --admin-cert or --admin-generate is required.")
}

// resolveAndValidateInitFlags resolves env-var fallbacks via viper and
// validates strategy-specific flag combinations. envPrefix selects the
// per-binary env var namespace (QRDB, XKMS, QUICBAAS, PLATFORMCTL).
func resolveAndValidateInitFlags(cmd *cobra.Command, f *initFlags, envPrefix string) error {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.AutomaticEnv()

	bindings := map[string]string{
		"so-pin":           "SO_PIN",
		"user-pin":         "USER_PIN",
		"barrier-strategy": "BARRIER_STRATEGY",
		"passphrase":       "PASSPHRASE",
		"shares":           "SHARES",
		"threshold":        "THRESHOLD",
		"hostname":         "HOSTNAME",
		"sans":             "SANS",
	}
	for flagName, envKey := range bindings {
		_ = v.BindEnv(flagName, envPrefix+"_"+envKey)
		if !cmd.Flags().Changed(flagName) {
			if val := v.GetString(flagName); val != "" {
				if err := cmd.Flags().Set(flagName, val); err != nil {
					return &InvalidArgError{Arg: flagName, Reason: err.Error()}
				}
			}
		}
	}

	if f.soPin == "" {
		return &MissingArgError{Arg: "--so-pin"}
	}
	if f.userPin == "" {
		return &MissingArgError{Arg: "--user-pin"}
	}
	if _, ok := validBarrierStrategies[f.barrierStrategy]; !ok {
		return &InvalidArgError{
			Arg:    "--barrier-strategy",
			Reason: "must be one of none, passphrase, shamir, pkcs11, tpm2, awskms, gcpkms, azurekv, vault",
		}
	}
	if _, needsPassphrase := barrierStrategiesNeedingPassphrase[f.barrierStrategy]; needsPassphrase {
		if f.passphrase == "" {
			return &MissingArgError{Arg: "--passphrase (required for --barrier-strategy=" + f.barrierStrategy + ")"}
		}
	} else if f.passphrase != "" {
		return &InvalidArgError{
			Arg:    "--passphrase",
			Reason: "only valid when --barrier-strategy=passphrase",
		}
	}
	if f.barrierStrategy == "shamir" {
		if f.shares <= 1 {
			return &InvalidArgError{Arg: "--shares", Reason: "required and must be >= 2 when --barrier-strategy=shamir"}
		}
		if f.threshold <= 1 || f.threshold > f.shares {
			return &InvalidArgError{Arg: "--threshold", Reason: "required and must satisfy 2 <= threshold <= shares when --barrier-strategy=shamir"}
		}
	} else if f.shares != 0 || f.threshold != 0 {
		return &InvalidArgError{
			Arg:    "--shares/--threshold",
			Reason: "only valid when --barrier-strategy=shamir",
		}
	}
	adminPathsSelected := 0
	if f.adminCSR != "" {
		adminPathsSelected++
	}
	if f.adminCert != "" {
		adminPathsSelected++
	}
	if f.adminGenerate {
		adminPathsSelected++
	}
	if adminPathsSelected > 1 {
		return &InvalidArgError{
			Arg:    "--admin-csr/--admin-cert/--admin-generate",
			Reason: "mutually exclusive: supply exactly one admin credential path",
		}
	}
	if adminPathsSelected == 0 {
		return &InvalidArgError{
			Arg:    "--admin-csr/--admin-cert/--admin-generate",
			Reason: "required: there is no credential-less default admin credential path; pass --admin-generate for the dev/test/evaluation ONLY path",
		}
	}
	if f.adminCertPool != "" && f.adminCert == "" {
		return &InvalidArgError{Arg: "--admin-cert-pool", Reason: "only valid with --admin-cert"}
	}
	if f.officersFile != "" && (len(f.officers) > 0 || len(f.custodians) > 0) {
		return &InvalidArgError{
			Arg:    "--officers-file",
			Reason: "mutually exclusive with --officers/--custodians: name every holder one way or the other, not both",
		}
	}
	return nil
}

// parseOfficerFlags parses repeatable holder flag values
// ("username=path/to/csr.pem") of one role into the ceremony's holder
// request shape, reading each named CSR file from disk. flag names the flag
// for error messages.
func parseOfficerFlags(flag, role string, raw []string) ([]transport.CeremonyOfficerRequest, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	officers := make([]transport.CeremonyOfficerRequest, 0, len(raw))
	for _, entry := range raw {
		username, csrPath, ok := strings.Cut(entry, "=")
		if !ok || username == "" || csrPath == "" {
			return nil, &InvalidArgError{
				Arg:    flag,
				Reason: "must be in the form username=path/to/csr.pem, got " + entry,
			}
		}
		csrPEM, err := os.ReadFile(csrPath)
		if err != nil {
			return nil, &FileReadError{Path: csrPath, Err: err}
		}
		officers = append(officers, transport.CeremonyOfficerRequest{
			Username: username,
			Role:     role,
			CSRPEM:   string(csrPEM),
		})
	}
	return officers, nil
}

// runInit dispatches the validated init flags to the SDK CeremonyService.
// Holder identities are CA-assigned over CSR public keys; the server never
// holds a holder's private key, so there is no credential bundle to deliver.
//
// Exactly one admin-credential path reaches the server (go-qrdb#548
// review): --admin-csr (CSR-sign, the admin's key never leaves this
// machine), --admin-cert (bring-your-own-certificate, nothing is issued),
// or --admin-generate (the server issues a key pair and returns the key
// exactly once). resolveAndValidateInitFlags already refused a request
// selecting none or more than one of the three before runInit is ever
// reached. The generate path is dev/test/evaluation only; runInit warns
// loudly when it is used.
func runInit(cmd *cobra.Command, f *initFlags) error {
	var officers []transport.CeremonyOfficerRequest
	if f.officersFile != "" {
		// --officers-file is the rich-holder path (go-qrdb#4578 review
		// item 1): separate envelope keys, extra factors, and the
		// designated factor, none of which username=path/to/csr.pem can
		// express. resolveAndValidateInitFlags already refused combining
		// it with --officers/--custodians.
		var err error
		officers, err = parseOfficersFile(f.officersFile)
		if err != nil {
			return err
		}
	} else {
		var err error
		officers, err = parseOfficerFlags("--officers", transport.RoleSecurityOfficer, f.officers)
		if err != nil {
			return err
		}
		custodians, cerr := parseOfficerFlags("--custodians", transport.RoleCustodian, f.custodians)
		if cerr != nil {
			return cerr
		}
		officers = append(officers, custodians...)
	}
	// --shares is N, and the key is split to exactly the named holders, so
	// the two must agree. With no holders named the ceremony's own M-of-N
	// validation refuses the request.
	if len(officers) > 0 && len(officers) != f.shares {
		return &InvalidArgError{
			Arg:    "--shares",
			Reason: "must equal the number of --officers plus --custodians (" + strconv.Itoa(len(officers)) + ")",
		}
	}

	// f.passphrase is the one, unavoidable copy at the CLI flag-parsing
	// boundary: cobra owns that Go string, and nothing converts it to
	// wipeable bytes before this point. Wiped once the request below is
	// sent (go-qrdb#560 review item 7, follow-up).
	secretBytes := []byte(f.passphrase)
	defer secure.Wipe(secretBytes)
	req := &transport.CeremonyInitializeRequest{
		SOPin:                  f.soPin,
		UserPin:                f.userPin,
		Threshold:              f.threshold,
		Policy:                 f.policy,
		CredentialSealStrategy: f.barrierStrategy,
		Officers:               officers,
		Hostname:               f.hostname,
		Secret:                 secretBytes,
	}
	if f.sans != "" {
		req.SANs = strings.Split(f.sans, ",")
	}
	switch {
	case f.adminCSR != "":
		pem, rerr := os.ReadFile(f.adminCSR)
		if rerr != nil {
			return &FileReadError{Path: f.adminCSR, Err: rerr}
		}
		req.AdminCSRPEM = string(pem)
	case f.adminCert != "":
		pem, rerr := os.ReadFile(f.adminCert)
		if rerr != nil {
			return &FileReadError{Path: f.adminCert, Err: rerr}
		}
		req.AdminCertPEM = string(pem)
		if f.adminCertPool != "" {
			poolPEM, perr := os.ReadFile(f.adminCertPool)
			if perr != nil {
				return &FileReadError{Path: f.adminCertPool, Err: perr}
			}
			req.AdminCertPoolPEM = string(poolPEM)
		}
	case f.adminGenerate:
		req.AdminGenerate = true
		// This warning is operator-facing prose, not part of the command's
		// output contract, so it goes to stderr even under --output
		// json/yaml: outputWriter carries ONLY the response (go-qrdb#548).
		// A caller parsing stdout as JSON/YAML (the CLI's own --output json
		// contract, and every integration-test helper that decodes it) would
		// otherwise fail on this line.
		_, _ = fmt.Fprintln(os.Stderr, "WARNING: --admin-generate given; the server will GENERATE "+
			"the bootstrap admin's key pair and return the private key below, exactly once. This path is for "+
			"dev/test/evaluation ONLY and does NOT meet FedRAMP, HIPAA or PCI DSS: use --admin-csr (a hardware- or "+
			"software-generated key you keep) or --admin-cert (a certificate you already hold) for production.")
	}

	resp, err := client.Ceremony().CeremonyInitialize(cmd.Context(), req)
	if err != nil {
		return err
	}
	// resp.AdminKeyPEM, when present, is returned exactly once and wiped
	// here once this command has finished printing it in whichever format
	// the caller asked for (go-qrdb#560 review item 7, follow-up).
	defer secure.Wipe(resp.AdminKeyPEM)

	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "State:       %s\n", resp.State)
		_, _ = fmt.Fprintf(outputWriter, "SPKIPin:     %s\n", resp.SPKIPin)
		_, _ = fmt.Fprintf(outputWriter, "ShareCount:  %d\n", resp.ShareCount)
		for _, h := range resp.Holders {
			_, _ = fmt.Fprintf(outputWriter, "Holder:      %s (%s)\n%s\n", h.Username, h.Role, h.CertPEM)
		}
		if resp.AdminCertPEM != "" {
			_, _ = fmt.Fprintf(outputWriter, "AdminCertPEM (source=%s):\n%s\n", resp.AdminCredentialSource, resp.AdminCertPEM)
		}
		if len(resp.AdminKeyPEM) != 0 {
			_, _ = fmt.Fprintln(outputWriter, "AdminKeyPEM (returned exactly once -- save it now, it cannot be retrieved again):")
			_, _ = fmt.Fprintln(outputWriter, string(resp.AdminKeyPEM))
		}
		return nil
	}
}
