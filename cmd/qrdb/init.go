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
// docs/08-bootstrap-auth-architecture.md §6. The command bootstraps the
// barrier, CA, admin certificate and roles in a single call, delegating
// to the qrdb CeremonyService over the SDK transport.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// validBarrierStrategies is the set of supported --barrier-strategy values
// per docs/08-bootstrap-auth-architecture.md §2.
var validBarrierStrategies = map[string]struct{}{
	"passphrase": {},
	"shamir":     {},
	"pkcs11":     {},
	"tpm2":       {},
	"awskms":     {},
	"gcpkms":     {},
	"azurekv":    {},
	"vault":      {},
}

// initFlags holds the canonical init flag set shared by all four binaries.
type initFlags struct {
	soPin           string
	userPin         string
	barrierStrategy string
	shares          int
	threshold       int
	hostname        string
	sans            string
	policy          string
	officers        []string
	custodians      []string
}

// newInitCmd returns the canonical root-level "init" command.
//
// Per the spec it bootstraps barrier + CA + admin cert + roles in one
// call. Per CLI Surface Convergence (§6) the same command, the same
// flag set and the same env-var bindings appear on every binary.
func newInitCmd() *cobra.Command {
	f := &initFlags{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the barrier, CA, admin cert and roles in one call",
		Long: `Bootstrap a fresh qrdb system in one call.

The init command delegates to the qrdb CeremonyService and creates the
barrier, the CA, the initial admin certificate and the default role set
in a single transactional ceremony.

Canonical environment variables (Viper-bound):
  QRDB_SO_PIN              equivalent to --so-pin
  QRDB_USER_PIN            equivalent to --user-pin
  QRDB_BARRIER_STRATEGY    equivalent to --barrier-strategy
  QRDB_SHARES              equivalent to --shares
  QRDB_THRESHOLD           equivalent to --threshold
  QRDB_HOSTNAME            equivalent to --hostname
  QRDB_SANS                equivalent to --sans

M-of-N Shamir custody (--barrier-strategy shamir with --threshold >= 2)
names the initial share holders up front: Security Officers via --officers
and key custodians via --custodians (both repeatable), each entry
username=path/to/csr.pem, where the CSR supplies only the holder's public
key — the node's CA assigns the certificate identity. Officers and
custodians are enrolled the same way and together make up --shares. Each
holder then claims its sealed share envelope with 'barrier officer claim'.

Examples:
  qrdb init --so-pin SOPIN --user-pin USERPIN
  qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
            --so-pin SOPIN --user-pin USERPIN \
            --officers alice=alice.csr --officers bob=bob.csr \
            --custodians carol=carol.csr --custodians dave=dave.csr \
            --custodians erin=erin.csr
  QRDB_SO_PIN=... QRDB_USER_PIN=... qrdb init`,
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
	cmd.Flags().IntVar(&f.shares, "shares", 0,
		"Total Shamir shares (required when --barrier-strategy=shamir)")
	cmd.Flags().IntVar(&f.threshold, "threshold", 0,
		"Shamir M-of-N threshold (required when --barrier-strategy=shamir)")
	cmd.Flags().StringVar(&f.hostname, "hostname", "",
		"FQDN written into the admin/server certificate CN")
	cmd.Flags().StringVar(&f.sans, "sans", "",
		"Comma-separated additional DNS / IP SANs")
	cmd.Flags().StringArrayVar(&f.officers, "officers", nil,
		"Security Officer share holder: username=path/to/csr.pem (repeatable)")
	cmd.Flags().StringArrayVar(&f.custodians, "custodians", nil,
		"Key custodian share holder: username=path/to/csr.pem (repeatable)")
	cmd.Flags().StringVar(&f.policy, "policy", "",
		"Factor policy of the initial share set: designated (default) or any-enrolled")
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
			Reason: "must be one of passphrase, shamir, pkcs11, tpm2, awskms, gcpkms, azurekv, vault",
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
func runInit(cmd *cobra.Command, f *initFlags) error {
	officers, err := parseOfficerFlags("--officers", transport.RoleSecurityOfficer, f.officers)
	if err != nil {
		return err
	}
	custodians, err := parseOfficerFlags("--custodians", transport.RoleCustodian, f.custodians)
	if err != nil {
		return err
	}
	officers = append(officers, custodians...)
	// --shares is N, and the key is split to exactly the named holders, so
	// the two must agree. With no holders named the ceremony's own M-of-N
	// validation refuses the request.
	if len(officers) > 0 && len(officers) != f.shares {
		return &InvalidArgError{
			Arg:    "--shares",
			Reason: "must equal the number of --officers plus --custodians (" + strconv.Itoa(len(officers)) + ")",
		}
	}

	req := &transport.CeremonyInitializeRequest{
		SOPin:                  f.soPin,
		UserPin:                f.userPin,
		Threshold:              f.threshold,
		Policy:                 f.policy,
		CredentialSealStrategy: f.barrierStrategy,
		Officers:               officers,
		Hostname:               f.hostname,
	}
	if f.sans != "" {
		req.SANs = strings.Split(f.sans, ",")
	}

	resp, err := client.Ceremony().CeremonyInitialize(cmd.Context(), req)
	if err != nil {
		return err
	}

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
		return nil
	}
}
