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

// barrier.go implements the canonical barrier verb tree shared by every
// CLI binary in the stack (qrdb / xkmsctl / quicbaasctl / platformctl).
// The surface and flag set defined here are the gold-standard the other
// three binaries MUST mirror exactly.
//
// Canonical verb tree:
//
//	barrier status                                       (read-only)
//	barrier init           --barrier-strategy <none|passphrase|shamir>
//	                       [--passphrase <passphrase>]  (required for passphrase/shamir)
//	                       [--so-pin <pin>]             (requires --user-pin; rejected when strategy=none)
//	                       [--user-pin <pin>]           (requires --so-pin; rejected when strategy=none)
//	barrier init-shamir    --passphrase <passphrase>
//	                       --threshold N
//	                       --total M
//	                       [--so-pin] [--user-pin]
//	barrier unseal         --passphrase <passphrase>    (strategy passphrase replay)
//	barrier unseal-shares  --share <s1> [--share <s2> ...]   (repeatable)
//	                       | --shares-file <path>
//	barrier seal           [--so-pin <pin>] [-y|--assume-yes]
//	barrier rekey          [--so-pin <pin>]
//	                       [--threshold N --total M]    (paired; no secret on the wire)
//	barrier recovery generate   --threshold N --total M [--so-pin <pin>]
//	barrier recovery recover    --share <s1> [--share <s2> ...]
//
// Operational sub-trees that are not part of the canonical seven verbs
// (`barrier shamir ...` share management and `barrier root-token`) are
// retained for operational parity but are explicitly NOT required of
// the other three binaries.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// barrierStrategyInit enumerates the strategies accepted by `barrier
// init`. The root-level `init` ceremony accepts the full strategy set
// (pkcs11, tpm2, awskms, gcpkms, azurekv, vault) because it provisions
// the CA and PIN-store in addition to the barrier; the dedicated
// `barrier init` verb is intentionally limited to the three software-
// observable strategies. Map lookup is O(1).
var barrierStrategyInit = map[string]struct{}{
	"none":       {},
	"passphrase": {},
	"shamir":     {},
}

// newBarrierCmd returns the canonical barrier command group.
func newBarrierCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "barrier",
		Short: "Cryptographic barrier management (canonical CLI surface)",
		Long: `Canonical barrier command group.

The verb tree exposed here is the gold-standard surface shared by every
CLI binary in the stack (qrdb, xkmsctl, quicbaasctl, platformctl). The
seven canonical verbs are:

  status         Show barrier state
  init           Initialize barrier with passphrase, none, or shamir-by-secret
  init-shamir    Initialize barrier with M-of-N Shamir shares
  unseal         Unseal using a passphrase
  unseal-shares  Unseal using one or more Shamir shares
  seal           Seal the barrier
  rekey          Rotate barrier key material
  recovery       Generate or use recovery keys

Operational sub-trees ("shamir", "root-token") are retained for ops
parity but are not part of the canonical surface.`,
	}

	cmd.AddCommand(
		newBarrierStatusCmd(),
		newBarrierInitCmd(),
		newBarrierInitShamirCmd(),
		newBarrierUnsealCmd(),
		newBarrierUnsealSharesCmd(),
		newBarrierSealCmd(),
		newBarrierShamirSealVoteCmd(),
		newBarrierRekeyCmd(),
		newBarrierEnableEncryptionCmd(),
		newBarrierRecoveryCmd(),
		newBarrierOfficerCmd(),
		newBarrierRootTokenCmd(),
	)

	return cmd
}

// ---------------------------------------------------------------------------
// barrier status
// ---------------------------------------------------------------------------

// newBarrierStatusCmd returns the canonical `barrier status` subcommand.
// It is read-only and honors the persistent --output flag.
func newBarrierStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show barrier status",
		Long: `Show the current state of the cryptographic barrier.

Output is governed by the persistent --output flag (table | json | yaml).
This command is read-only and safe to call against a sealed barrier.

Example:
  qrdb barrier status
  qrdb --output json barrier status`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Barrier().BarrierStatus(cmd.Context())
			if err != nil {
				return err
			}
			return renderBarrierStatus(resp)
		},
	}
}

// renderBarrierStatus prints a BarrierStatusResponse in the configured
// output format. Extracted for clarity and reuse from tests.
func renderBarrierStatus(resp *transport.BarrierStatusResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Sealed:          %t\n", resp.Sealed)
		_, _ = fmt.Fprintf(outputWriter, "Strategy:        %s\n", resp.Strategy)
		_, _ = fmt.Fprintf(outputWriter, "HardwareBacked:  %t\n", resp.HardwareBacked)
		_, _ = fmt.Fprintf(outputWriter, "InitializedAt:   %s\n", resp.InitializedAt)
		return nil
	}
}

// ---------------------------------------------------------------------------
// barrier init
// ---------------------------------------------------------------------------

// barrierInitFlags holds the flag set for `barrier init`. Keeping the
// flags on a struct lets unit tests construct identical input vectors
// without poking package-level state.
type barrierInitFlags struct {
	strategy   string
	passphrase string
	soPin      string
	userPin    string
}

// newBarrierInitCmd returns the canonical `barrier init` subcommand.
//
// Unlike the root `init` command (which runs the full ceremony --
// barrier + CA + admin cert + roles), this verb initializes only the
// barrier subsystem. Use this when bolting a barrier onto an existing
// cluster or when the CA / IAM has already been provisioned.
func newBarrierInitCmd() *cobra.Command {
	f := &barrierInitFlags{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the cryptographic barrier",
		Long: `Initialize the cryptographic barrier.

Strategies:
  none        Identity passthrough; data stored in cleartext (dev only).
  passphrase  Single passphrase-derived master key (default).
  shamir      Single-secret Shamir seed; use 'barrier init-shamir' for M-of-N.

--passphrase is required when --barrier-strategy is passphrase or shamir
and rejected when --barrier-strategy is none. PINs are optional but
follow the dual-control rule: supplying --so-pin requires --user-pin
(and vice versa) so the resulting PIN pair satisfies FIPS 140-3 /
PCI-DSS v4 dual-control. PINs are rejected entirely when
--barrier-strategy=none (typed error PINsNotAllowedError).

Examples:
  qrdb barrier init --barrier-strategy passphrase --passphrase '<passphrase>'
  qrdb barrier init --barrier-strategy none
  qrdb barrier init --barrier-strategy passphrase --passphrase '<pp>' \
                    --so-pin SO123 --user-pin USR123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateBarrierInitFlags(f); err != nil {
				return err
			}
			req := &transport.BarrierInitializeRequest{
				Secret:          f.passphrase,
				BarrierStrategy: f.strategy,
				SOPin:           f.soPin,
				UserPin:         f.userPin,
			}
			if err := client.Barrier().BarrierInitialize(cmd.Context(), req); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&f.strategy, "barrier-strategy", "passphrase",
		"Barrier strategy: none | passphrase | shamir")
	cmd.Flags().StringVar(&f.passphrase, "passphrase", "",
		"Master passphrase (required for passphrase and shamir)")
	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (optional; requires --user-pin)")
	cmd.Flags().StringVar(&f.userPin, "user-pin", "",
		"User PIN (optional; requires --so-pin)")

	return cmd
}

// validateBarrierInitFlags enforces the canonical input contract for
// `barrier init`. Validation runs before any network call so operators
// see actionable errors locally.
func validateBarrierInitFlags(f *barrierInitFlags) error {
	if _, ok := barrierStrategyInit[f.strategy]; !ok {
		return &InvalidArgError{
			Arg:    "--barrier-strategy",
			Reason: "must be one of none, passphrase, shamir",
		}
	}
	if f.strategy != "none" && f.passphrase == "" {
		return &MissingArgError{Arg: "--passphrase (required for --barrier-strategy=" + f.strategy + ")"}
	}
	if f.strategy == "none" && f.passphrase != "" {
		return &InvalidArgError{
			Arg:    "--passphrase",
			Reason: "not allowed when --barrier-strategy=none",
		}
	}
	if f.strategy == "none" && (f.soPin != "" || f.userPin != "") {
		return &PINsNotAllowedError{Strategy: "none", Op: "init"}
	}
	if f.userPin != "" && f.soPin == "" {
		return &InvalidArgError{
			Arg:    "--user-pin",
			Reason: "requires --so-pin",
		}
	}
	if f.soPin != "" && f.userPin == "" {
		return &InvalidArgError{
			Arg:    "--so-pin",
			Reason: "requires --user-pin (FIPS 140-3 / PCI-DSS v4 dual-control)",
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// barrier init-shamir
// ---------------------------------------------------------------------------

// barrierInitShamirFlags holds the flag set for `barrier init-shamir`.
type barrierInitShamirFlags struct {
	passphrase string
	threshold  int
	total      int
	soPin      string
	userPin    string
}

// newBarrierInitShamirCmd returns the canonical `barrier init-shamir`
// subcommand. It initializes the barrier with Shamir M-of-N secret
// sharing; the response is share-free (INV-1) — shares are sealed to
// the enrolled Security Officers' enclaves for one-time claim.
func newBarrierInitShamirCmd() *cobra.Command {
	f := &barrierInitShamirFlags{}

	cmd := &cobra.Command{
		Use:   "init-shamir",
		Short: "Initialize the barrier with Shamir M-of-N secret sharing",
		Long: `Initialize the barrier using Shamir M-of-N secret sharing.

The master secret is split into --total shares of which --threshold are
required to unseal. PIN flags are optional but follow the same dual-
control rule as 'barrier init' -- supplying --so-pin requires
--user-pin.

The response is share-free: shares are sealed server-side to the
enrolled Security Officers' enclaves. Each officer retrieves their
share exactly once via 'barrier officer claim-begin' / 'claim-complete'
using their own mTLS certificate.

Example:
  qrdb barrier init-shamir --passphrase '<seed>' --threshold 3 --total 5`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateBarrierInitShamirFlags(f); err != nil {
				return err
			}
			req := &transport.BarrierInitializeShamirRequest{
				Secret:      f.passphrase,
				Threshold:   f.threshold,
				TotalShares: f.total,
				SOPin:       f.soPin,
				UserPin:     f.userPin,
			}
			resp, err := client.Barrier().BarrierInitializeShamir(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderShamirInit(resp)
		},
	}

	cmd.Flags().StringVar(&f.passphrase, "passphrase", "", "Master passphrase (required)")
	cmd.Flags().IntVar(&f.threshold, "threshold", 0, "Minimum shares required to unseal (M)")
	cmd.Flags().IntVar(&f.total, "total", 0, "Total number of shares to generate (N)")
	cmd.Flags().StringVar(&f.soPin, "so-pin", "", "Security Officer PIN (optional; requires --user-pin)")
	cmd.Flags().StringVar(&f.userPin, "user-pin", "", "User PIN (optional; requires --so-pin)")
	_ = cmd.MarkFlagRequired("passphrase")
	_ = cmd.MarkFlagRequired("threshold")
	_ = cmd.MarkFlagRequired("total")

	return cmd
}

// validateBarrierInitShamirFlags enforces Shamir-specific invariants.
func validateBarrierInitShamirFlags(f *barrierInitShamirFlags) error {
	if f.threshold < 2 {
		return &InvalidArgError{Arg: "--threshold", Reason: "must be >= 2"}
	}
	if f.total < f.threshold {
		return &InvalidArgError{Arg: "--total", Reason: "must be >= --threshold"}
	}
	if f.userPin != "" && f.soPin == "" {
		return &InvalidArgError{Arg: "--user-pin", Reason: "requires --so-pin"}
	}
	if f.soPin != "" && f.userPin == "" {
		return &InvalidArgError{
			Arg:    "--so-pin",
			Reason: "requires --user-pin (FIPS 140-3 / PCI-DSS v4 dual-control)",
		}
	}
	return nil
}

// renderShamirInit prints a BarrierInitializeShamirResponse in the
// configured output format. Extracted so `rekey` can reuse the rendering
// (it returns the same shape).
func renderShamirInit(resp *transport.BarrierInitializeShamirResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Threshold:    %d\n", resp.Threshold)
		_, _ = fmt.Fprintf(outputWriter, "TotalShares:  %d\n", resp.TotalShares)
		_, _ = fmt.Fprintf(outputWriter, "ShareCount:   %d\n", resp.ShareCount)
		_, _ = fmt.Fprintln(outputWriter, "Shares are sealed to enrolled Security Officers.")
		_, _ = fmt.Fprintln(outputWriter, "Each officer must claim their share via 'barrier officer claim-begin'.")
		return nil
	}
}

// ---------------------------------------------------------------------------
// barrier unseal (passphrase replay)
// ---------------------------------------------------------------------------

// newBarrierUnsealCmd returns the canonical `barrier unseal` subcommand
// for the passphrase / single-secret strategy. Shamir share submission
// is handled by the separate `barrier unseal-shares` verb.
func newBarrierUnsealCmd() *cobra.Command {
	var passphrase string

	cmd := &cobra.Command{
		Use:   "unseal",
		Short: "Unseal the barrier using a passphrase",
		Long: `Unseal the barrier using the master passphrase that was supplied at
'barrier init' time.

This verb is reserved for the passphrase / single-secret strategy. For
Shamir M-of-N unseal use 'barrier unseal-shares' with --share or
--shares-file.

Example:
  qrdb barrier unseal --passphrase '<passphrase>'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.BarrierUnsealRequest{Secret: passphrase}
			if err := client.Barrier().BarrierUnseal(cmd.Context(), req); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&passphrase, "passphrase", "", "Master passphrase")
	_ = cmd.MarkFlagRequired("passphrase")

	return cmd
}

// ---------------------------------------------------------------------------
// barrier unseal-shares (Shamir incremental / batch unseal)
// ---------------------------------------------------------------------------

// barrierUnsealSharesFlags holds the flag set for `barrier unseal-shares`.
type barrierUnsealSharesFlags struct {
	shares     []string
	sharesFile string
}

// newBarrierUnsealSharesCmd returns the canonical
// `barrier unseal-shares` subcommand. Operators may supply one or more
// `--share` flags (repeatable) or point at a `--shares-file` with one
// share per line. The two forms are mutually exclusive.
func newBarrierUnsealSharesCmd() *cobra.Command {
	f := &barrierUnsealSharesFlags{}

	cmd := &cobra.Command{
		Use:   "unseal-shares",
		Short: "Unseal the barrier by submitting Shamir shares",
		Long: `Unseal the barrier using one or more Shamir shares.

Two input modes are supported. Exactly one must be supplied:

  --share <value>           Repeatable. Supply one or more shares as flags.
  --shares-file <path>      Newline-delimited file of shares. Use '-' for stdin.

Submitting a number of shares >= the M threshold unseals the barrier in
one call. Submitting fewer shares accumulates them toward the threshold;
incremental progress is reported in the response.

Examples:
  qrdb barrier unseal-shares --share aaa --share bbb --share ccc
  qrdb barrier unseal-shares --shares-file /run/shamir.txt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			shares, err := resolveUnsealShares(f)
			if err != nil {
				return err
			}
			req := &transport.BarrierUnsealSharesRequest{Shares: shares}
			if err := client.Barrier().BarrierUnsealWithShares(cmd.Context(), req); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	// StringArrayVar (not StringSliceVar): a claim-complete JSON share
	// envelope contains commas, which StringSliceVar would mis-split.
	cmd.Flags().StringArrayVar(&f.shares, "share", nil,
		"Single Shamir share, or the JSON envelope 'barrier officer claim-complete' prints (repeatable: --share a --share b ...)")
	cmd.Flags().StringVar(&f.sharesFile, "shares-file", "",
		"Path to a newline-delimited share file; '-' for stdin")

	return cmd
}

// resolveUnsealShares enforces the --share / --shares-file invariants
// and returns the flattened share list. Each share is passed through
// resolveShareInput so a claim-complete JSON envelope round-trips
// straight into --share/--shares-file with no manual unwrap step.
func resolveUnsealShares(f *barrierUnsealSharesFlags) ([]string, error) {
	hasFlag := len(f.shares) > 0
	hasFile := f.sharesFile != ""
	if hasFlag && hasFile {
		return nil, &InvalidArgError{
			Arg:    "--share/--shares-file",
			Reason: "exactly one of --share (repeatable) or --shares-file is allowed",
		}
	}
	if !hasFlag && !hasFile {
		return nil, &MissingArgError{Arg: "--share or --shares-file"}
	}
	if hasFlag {
		return resolveShareInputs(f.shares)
	}
	shares, err := readSharesFile(f.sharesFile)
	if err != nil {
		return nil, err
	}
	return resolveShareInputs(shares)
}

// readSharesFile reads newline-delimited shares from path. The literal
// path "-" reads from stdin. Blank lines and lines whose first
// non-whitespace character is '#' are skipped to support commented
// share manifests.
func readSharesFile(path string) ([]string, error) {
	var src *os.File
	if path == "-" {
		src = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, &FileReadError{Path: path, Err: err}
		}
		defer func() { _ = f.Close() }()
		src = f
	}

	var shares []string
	scanner := bufio.NewScanner(src)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		shares = append(shares, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, &FileReadError{Path: path, Err: err}
	}
	if len(shares) == 0 {
		return nil, &InvalidArgError{
			Arg:    "--shares-file",
			Reason: "no shares found in " + path,
		}
	}
	return shares, nil
}

// ---------------------------------------------------------------------------
// barrier seal
// ---------------------------------------------------------------------------

// barrierSealFlags holds the flag set for `barrier seal`.
type barrierSealFlags struct {
	soPin     string
	assumeYes bool
}

// newBarrierSealCmd returns the canonical `barrier seal` subcommand.
// Sealing is destructive (reads stop until the next unseal) so the
// command prompts for confirmation unless `-y/--assume-yes` is set.
func newBarrierSealCmd() *cobra.Command {
	f := &barrierSealFlags{}

	cmd := &cobra.Command{
		Use:   "seal",
		Short: "Seal the cryptographic barrier",
		Long: `Seal the barrier. Once sealed, the data path returns ErrBarrierSealed
for every read and write until the barrier is unsealed again.

Operators with a PINBackend configured MUST supply --so-pin. The command
prompts for confirmation on a TTY; pass -y / --assume-yes from scripts
or non-interactive contexts.

Examples:
  qrdb barrier seal -y
  qrdb barrier seal --so-pin SO123 --assume-yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !f.assumeYes {
				if err := confirmDestructive(cmd, "Seal the barrier? This blocks I/O until the next unseal."); err != nil {
					return err
				}
			}
			// The --so-pin flag is parsed for surface parity with the
			// canonical CLI tree; PIN propagation rides on the SDK auth
			// context (same model the ceremony surface uses) once the
			// server-side handler honors it. Accepting the flag here
			// keeps the four-CLI surface lock-step.
			_ = f.soPin
			if err := client.Barrier().BarrierSeal(cmd.Context()); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (required when a PINBackend is configured)")
	cmd.Flags().BoolVarP(&f.assumeYes, "assume-yes", "y", false,
		"Skip the interactive confirmation prompt")

	return cmd
}

// barrierEnableEncryptionFlags holds the flags for `barrier enable-encryption`.
type barrierEnableEncryptionFlags struct {
	tenant       string
	strategy     string
	mechanism    string
	availability string
	passphrase   string
	threshold    int
	total        int
	assumeYes    bool
}

// newBarrierEnableEncryptionCmd returns the `barrier enable-encryption`
// subcommand, which migrates a previously-unencrypted barrier to a keyed
// strategy in place. Scope is the system barrier by default, or a tenant's
// dedicated barrier when --tenant is set.
func newBarrierEnableEncryptionCmd() *cobra.Command {
	f := &barrierEnableEncryptionFlags{}

	cmd := &cobra.Command{
		Use:   "enable-encryption",
		Short: "Enable at-rest encryption on a previously-unencrypted barrier",
		Long: `Migrate a barrier that was initialized without encryption (strategy "none")
to a keyed strategy, re-encrypting all at-rest data in place.

The migration runs asynchronously on the server: a system-scope migration
recycles the node's data plane in process, so the command returns once the
migration has started. Poll "qrdb barrier status" until the strategy changes
from "none" to the chosen strategy to confirm completion.

Scope is the system barrier by default. Pass --tenant to migrate a single
tenant's dedicated barrier instead; a tenant on the shared system barrier
cannot change its encryption and the server rejects the request.

Examples:
  qrdb barrier enable-encryption --strategy passphrase --passphrase 'hunter2'
  qrdb barrier enable-encryption --tenant acme --strategy passphrase --passphrase 'pw'
  qrdb barrier enable-encryption --strategy shamir --shamir-threshold 2 --shamir-total 3 --passphrase 'pw'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.strategy == "" {
				return &MissingArgError{Arg: "--strategy"}
			}
			if !f.assumeYes {
				if err := confirmDestructive(cmd,
					"Enable encryption? This re-encrypts all at-rest data and briefly pauses the data plane."); err != nil {
					return err
				}
			}
			req := &transport.BarrierEnableEncryptionRequest{
				TenantID:        f.tenant,
				Strategy:        f.strategy,
				Mechanism:       f.mechanism,
				Availability:    f.availability,
				Passphrase:      f.passphrase,
				ShamirThreshold: f.threshold,
				ShamirTotal:     f.total,
			}
			resp, err := client.Barrier().BarrierEnableEncryption(cmd.Context(), req)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(outputWriter,
				"%s (scope: %s)\nPoll 'qrdb barrier status' until the strategy changes to confirm completion.\n",
				resp.Status, resp.Scope)
			return nil
		},
	}

	cmd.Flags().StringVar(&f.tenant, "tenant", "",
		"Tenant ID to scope the migration to its dedicated barrier (default: system barrier)")
	cmd.Flags().StringVar(&f.strategy, "strategy", "",
		"Target keyed strategy: passphrase or shamir (required)")
	cmd.Flags().StringVar(&f.mechanism, "mechanism", "swap",
		"Re-encryption mechanism: swap or in-place")
	cmd.Flags().StringVar(&f.availability, "availability", "quiesced",
		"Availability mode: quiesced or online")
	cmd.Flags().StringVar(&f.passphrase, "passphrase", "",
		"Unseal passphrase for the keyed barrier")
	cmd.Flags().IntVar(&f.threshold, "shamir-threshold", 0,
		"Shamir threshold (M) when --strategy shamir")
	cmd.Flags().IntVar(&f.total, "shamir-total", 0,
		"Shamir total shares (N) when --strategy shamir")
	cmd.Flags().BoolVarP(&f.assumeYes, "assume-yes", "y", false,
		"Skip the interactive confirmation prompt")

	return cmd
}

// confirmDestructive prints a y/N prompt and reads a single line from
// stdin. Any answer other than 'y' or 'yes' (case-insensitive) returns
// a typed UserAbortError. On a non-TTY stdin the function returns the
// abort error immediately so scripts that forget --assume-yes fail
// loudly instead of hanging.
func confirmDestructive(cmd *cobra.Command, prompt string) error {
	stdin := cmd.InOrStdin()
	if stdin == nil {
		stdin = os.Stdin
	}
	_, _ = fmt.Fprintf(outputWriter, "%s [y/N]: ", prompt)
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return &UserAbortError{Op: "barrier seal"}
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if answer != "y" && answer != "yes" {
		return &UserAbortError{Op: "barrier seal"}
	}
	return nil
}

// ---------------------------------------------------------------------------
// barrier rekey
// ---------------------------------------------------------------------------

// barrierRekeyFlags holds the flag set for `barrier rekey`.
type barrierRekeyFlags struct {
	soPin     string
	threshold int
	total     int
}

// newBarrierRekeyCmd returns the canonical `barrier rekey` subcommand.
// Rekey is a Shamir-only, server-side operation: the server generates a
// fresh unseal key and share set itself, cryptographically revoking all
// previously distributed shares. No secret material travels on the
// request.
func newBarrierRekeyCmd() *cobra.Command {
	f := &barrierRekeyFlags{}

	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Rotate the barrier Shamir unseal key and share set",
		Long: `Rotate the barrier's Shamir unseal key and share set.

The server generates a fresh unseal key, re-wraps the root key under it,
and seals new shares to the enrolled Security Officers. All previously
distributed shares are cryptographically revoked. --so-pin is required
when a PINBackend is configured. --threshold and --total instruct the
server to regenerate the share set with a new M/N; when omitted the
configured M/N is reused.

Example:
  qrdb barrier rekey --so-pin SO123
  qrdb barrier rekey --threshold 3 --total 5`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateBarrierRekeyFlags(f); err != nil {
				return err
			}
			req := &transport.BarrierRekeyRequest{
				SOPin:     f.soPin,
				Threshold: f.threshold,
				Total:     f.total,
			}
			resp, err := client.Barrier().BarrierRekey(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderRekey(resp)
		},
	}

	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (required when a PINBackend is configured)")
	cmd.Flags().IntVar(&f.threshold, "threshold", 0,
		"New Shamir threshold (required when --total > 0)")
	cmd.Flags().IntVar(&f.total, "total", 0,
		"New Shamir total share count (required when --threshold > 0)")

	return cmd
}

// validateBarrierRekeyFlags enforces the rekey input contract.
func validateBarrierRekeyFlags(f *barrierRekeyFlags) error {
	// Shamir flags are paired: supplying one requires the other.
	if (f.threshold > 0) != (f.total > 0) {
		return &InvalidArgError{
			Arg:    "--threshold/--total",
			Reason: "must be supplied together when rekeying Shamir shares",
		}
	}
	if f.threshold > 0 && f.threshold < 2 {
		return &InvalidArgError{Arg: "--threshold", Reason: "must be >= 2"}
	}
	if f.total > 0 && f.total < f.threshold {
		return &InvalidArgError{Arg: "--total", Reason: "must be >= --threshold"}
	}
	return nil
}

// renderRekey prints a BarrierRekeyResponse in the configured output
// format.
func renderRekey(resp *transport.BarrierRekeyResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Threshold:    %d\n", resp.Threshold)
		_, _ = fmt.Fprintf(outputWriter, "TotalShares:  %d\n", resp.TotalShares)
		_, _ = fmt.Fprintf(outputWriter, "ShareCount:   %d\n", resp.ShareCount)
		_, _ = fmt.Fprintln(outputWriter, "Shares are sealed to enrolled Security Officers.")
		_, _ = fmt.Fprintln(outputWriter, "Each officer must claim their share via 'barrier officer claim-begin'.")
		return nil
	}
}

// ---------------------------------------------------------------------------
// barrier recovery (canonical: generate, recover) + ops (delete, status)
// ---------------------------------------------------------------------------

// newBarrierRecoveryCmd returns the recovery parent command. The
// canonical surface is "generate | recover"; "delete" and "status" are
// retained for operational parity but are not part of the four-CLI
// gold-standard.
func newBarrierRecoveryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recovery",
		Short: "Recovery key management",
		Long: `Generate and use recovery keys for disaster recovery scenarios.

Canonical surface:
  recovery generate   Generate recovery keys (M-of-N)
  recovery recover    Restore the barrier from recovery keys

Operational verbs (not part of the canonical surface):
  recovery delete     Delete all stored recovery keys
  recovery status     Show whether recovery keys are present`,
	}

	cmd.AddCommand(
		newBarrierRecoveryGenerateCmd(),
		newBarrierRecoveryRecoverCmd(),
		newBarrierRecoveryDeleteCmd(),
		newBarrierRecoveryStatusCmd(),
		newBarrierRecoveryOfficerCmd(),
	)

	return cmd
}

// barrierRecoveryGenerateFlags holds the flag set for `barrier recovery
// generate`.
type barrierRecoveryGenerateFlags struct {
	threshold int
	total     int
	soPin     string
}

// newBarrierRecoveryGenerateCmd returns the canonical
// `barrier recovery generate` subcommand.
func newBarrierRecoveryGenerateCmd() *cobra.Command {
	f := &barrierRecoveryGenerateFlags{}

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate M-of-N recovery keys",
		Long: `Generate fresh recovery keys.

Recovery keys are independent of the barrier's unseal shares and live in
a parallel KMS slot. Generation requires a Security Officer PIN when a
PINBackend is configured.

Example:
  qrdb barrier recovery generate --threshold 3 --total 5 --so-pin SO123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.threshold < 2 {
				return &InvalidArgError{Arg: "--threshold", Reason: "must be >= 2"}
			}
			if f.total < f.threshold {
				return &InvalidArgError{Arg: "--total", Reason: "must be >= --threshold"}
			}
			req := &transport.BarrierGenerateRecoveryKeysRequest{
				Threshold: f.threshold,
				Total:     f.total,
				SOPin:     f.soPin,
			}
			resp, err := client.Barrier().BarrierGenerateRecoveryKeys(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderRecoveryKeys(resp)
		},
	}

	cmd.Flags().IntVar(&f.threshold, "threshold", 0, "Minimum keys required for recovery (M)")
	cmd.Flags().IntVar(&f.total, "total", 0, "Total number of recovery keys (N)")
	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (required when a PINBackend is configured)")
	_ = cmd.MarkFlagRequired("threshold")
	_ = cmd.MarkFlagRequired("total")

	return cmd
}

// renderRecoveryKeys prints a BarrierRecoveryKeysResponse in the
// configured output format.
func renderRecoveryKeys(resp *transport.BarrierRecoveryKeysResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Threshold:  %d\n", resp.Threshold)
		_, _ = fmt.Fprintf(outputWriter, "Total:      %d\n", resp.Total)
		_, _ = fmt.Fprintf(outputWriter, "ShareCount: %d\n", resp.ShareCount)
		_, _ = fmt.Fprintln(outputWriter, "Recovery shares are sealed to enrolled recovery officers.")
		_, _ = fmt.Fprintln(outputWriter, "Each officer must claim their share via 'barrier recovery officer claim-begin'.")
		return nil
	}
}

// newBarrierRecoveryRecoverCmd returns the canonical
// `barrier recovery recover` subcommand. It accepts one or more
// repeatable `--share` flags.
func newBarrierRecoveryRecoverCmd() *cobra.Command {
	var shares []string

	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Recover the barrier using recovery keys",
		Long: `Recover the barrier by submitting M-of-N recovery keys.

--share is repeatable: pass each recovery key as its own --share flag.

Example:
  qrdb barrier recovery recover --share key-a --share key-b --share key-c`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(shares) == 0 {
				return &MissingArgError{Arg: "--share (at least one)"}
			}
			resolved, err := resolveShareInputs(shares)
			if err != nil {
				return err
			}
			req := &transport.BarrierRecoverWithKeysRequest{Keys: resolved}
			if err := client.Barrier().BarrierRecoverWithKeys(cmd.Context(), req); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	// StringArrayVar (not StringSliceVar): a claim-complete JSON share
	// envelope contains commas, which StringSliceVar would mis-split.
	cmd.Flags().StringArrayVar(&shares, "share", nil,
		"Recovery key, or the JSON envelope 'barrier recovery officer claim-complete' prints (repeatable: --share a --share b ...)")
	_ = cmd.MarkFlagRequired("share")

	return cmd
}

// newBarrierRecoveryDeleteCmd is an operational verb retained for ops
// parity. It is intentionally NOT part of the canonical four-CLI
// surface.
func newBarrierRecoveryDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete",
		Short: "Delete all stored recovery keys (operational)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := client.Barrier().BarrierDeleteRecoveryKeys(cmd.Context()); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newBarrierRecoveryStatusCmd reports recovery-key presence. Retained
// for ops parity; not part of the canonical surface.
func newBarrierRecoveryStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether recovery keys are present (operational)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Barrier().BarrierHasRecoveryKeys(cmd.Context())
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "HasKeys: %t\n", resp.HasKeys)
				return nil
			}
		},
	}
}

// ---------------------------------------------------------------------------
// barrier root-token (operational; not part of canonical surface)
// ---------------------------------------------------------------------------

// newBarrierRootTokenCmd generates a root token from Shamir shares.
// Retained for ops parity; not part of the canonical surface.
func newBarrierRootTokenCmd() *cobra.Command {
	var shares []string

	cmd := &cobra.Command{
		Use:   "root-token",
		Short: "Generate a root token from Shamir shares (operational)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(shares) == 0 {
				return &MissingArgError{Arg: "--share (at least one)"}
			}
			resolved, err := resolveShareInputs(shares)
			if err != nil {
				return err
			}
			req := &transport.BarrierGenerateRootTokenRequest{Shares: resolved}
			resp, err := client.Barrier().BarrierGenerateRootToken(cmd.Context(), req)
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Token:      %s\n", resp.Token)
				_, _ = fmt.Fprintf(outputWriter, "CreatedAt:  %s\n", resp.CreatedAt)
				return nil
			}
		},
	}

	// StringArrayVar (not StringSliceVar): a claim-complete JSON share
	// envelope contains commas, which StringSliceVar would mis-split.
	cmd.Flags().StringArrayVar(&shares, "share", nil,
		"Shamir share, or the JSON envelope 'barrier officer claim-complete' prints (repeatable: --share a --share b ...)")
	_ = cmd.MarkFlagRequired("share")

	return cmd
}

// ---------------------------------------------------------------------------
// barrier shamir seal-vote
// ---------------------------------------------------------------------------

// newBarrierShamirSealVoteCmd returns the `barrier shamir-seal-vote`
// subcommand. Each enrolled SO submits their share as a vote to seal the
// barrier; once the quorum threshold is met the barrier seals automatically.
func newBarrierShamirSealVoteCmd() *cobra.Command {
	var share string

	cmd := &cobra.Command{
		Use:   "shamir-seal-vote",
		Short: "Submit a Shamir share as a seal vote (Shamir barriers only)",
		Long: `Submit a Shamir share as a quorum vote to seal the barrier.

When the quorum threshold is met all submitted shares are wiped and the
barrier transitions to the sealed state automatically.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveShareInput(share)
			if err != nil {
				return err
			}
			resp, err := client.Barrier().ShamirSealVote(cmd.Context(), &transport.ShamirSealVoteRequest{Share: resolved})
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				_, _ = fmt.Fprintf(outputWriter, "Required:   %d\n", resp.Required)
				_, _ = fmt.Fprintf(outputWriter, "Submitted:  %d\n", resp.Submitted)
				_, _ = fmt.Fprintf(outputWriter, "Complete:   %v\n", resp.Complete)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&share, "share", "", "Shamir share to submit as a seal vote")
	_ = cmd.MarkFlagRequired("share")

	return cmd
}

// ---------------------------------------------------------------------------
// Shamir share-envelope UX: claim-complete emits, and every share consumer
// (unseal-shares, shamir-seal-vote, root-token, recovery recover, and the
// tenant-barrier equivalents) accepts, either the bare base64 share value
// or the full JSON shamir.Share envelope. See resolveShareInput.
// ---------------------------------------------------------------------------

// shareEnvelope mirrors the JSON shape the ceremony's claim-complete
// endpoints (system officer, recovery officer, tenant officer) embed in
// ShareClaimCompleteResponse.ShareBytes:
// {"index":N,"threshold":M,"total":T,"value":"<base64>"}.
type shareEnvelope struct {
	Index     int    `json:"index"`
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
	Value     string `json:"value"`
}

// resolveShareInput accepts a Shamir/recovery share as either the bare
// base64 value a consumer command expects, or the full JSON shareEnvelope
// that 'barrier officer claim-complete' / 'barrier recovery officer
// claim-complete' / 'tenant barrier-officer claim-complete' return (see
// renderShareClaimComplete's "Share:" line). This lets an operator feed a
// freshly claimed share straight into unseal-shares, shamir-seal-vote,
// root-token, or recovery recover with no manual unwrap step. A value
// that does not look like a JSON object (i.e. does not start with '{')
// is returned unchanged, since base64 never begins with that character.
func resolveShareInput(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return raw, nil
	}
	var env shareEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return "", &InvalidArgError{
			Arg:    "--share",
			Reason: "looks like a JSON share envelope but failed to parse: " + err.Error(),
		}
	}
	if env.Value == "" {
		return "", &InvalidArgError{
			Arg:    "--share",
			Reason: "JSON share envelope has an empty \"value\" field",
		}
	}
	return env.Value, nil
}

// resolveShareInputs applies resolveShareInput to every element of raw,
// preserving order, and fails on the first malformed entry.
func resolveShareInputs(raw []string) ([]string, error) {
	resolved := make([]string, len(raw))
	for i, s := range raw {
		v, err := resolveShareInput(s)
		if err != nil {
			return nil, err
		}
		resolved[i] = v
	}
	return resolved, nil
}

// ---------------------------------------------------------------------------
// Shared officer/claim response renderers, reused by the system barrier
// officer surface (barrier officer / barrier recovery officer) and the
// tenant-scoped mirror (tenant barrier-officer).
// ---------------------------------------------------------------------------

// renderOfficerRegisterResponse prints an OfficerRegisterResponse in the
// configured output format.
func renderOfficerRegisterResponse(resp *transport.OfficerRegisterResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ShareIndex:  %d\n", resp.ShareIndex)
		_, _ = fmt.Fprintf(outputWriter, "CertPEM:\n%s\n", string(resp.CertPEM))
		return nil
	}
}

// renderOfficerStatusList prints a detailed, bulleted view of enrolled
// officers (used by the "list" verbs).
func renderOfficerStatusList(officers []*transport.OfficerStatus) error {
	switch outputFormat {
	case "json":
		return printJSON(officers)
	case "yaml":
		return printYAML(officers)
	default:
		for _, o := range officers {
			_, _ = fmt.Fprintf(outputWriter, "- Username:       %s\n", o.Username)
			_, _ = fmt.Fprintf(outputWriter, "  ShareIndex:     %d\n", o.ShareIndex)
			_, _ = fmt.Fprintf(outputWriter, "  AuthFactor:     %s\n", o.AuthFactor)
			_, _ = fmt.Fprintf(outputWriter, "  SharePending:   %v\n", o.SharePending)
			_, _ = fmt.Fprintf(outputWriter, "  EnrolledAt:     %s\n", o.EnrolledAt.Format("2006-01-02T15:04:05Z"))
			if !o.ClaimedAt.IsZero() {
				_, _ = fmt.Fprintf(outputWriter, "  ClaimedAt:      %s\n", o.ClaimedAt.Format("2006-01-02T15:04:05Z"))
			}
		}
		return nil
	}
}

// renderOfficerClaimStatusList prints a condensed, one-line-per-officer
// claim status view (used by the "claim-status" verbs).
func renderOfficerClaimStatusList(officers []*transport.OfficerStatus) error {
	switch outputFormat {
	case "json":
		return printJSON(officers)
	case "yaml":
		return printYAML(officers)
	default:
		for _, o := range officers {
			claimed := "no"
			if !o.ClaimedAt.IsZero() {
				claimed = o.ClaimedAt.Format("2006-01-02T15:04:05Z")
			}
			_, _ = fmt.Fprintf(outputWriter, "- %-20s  index=%d  pending=%v  claimed=%s\n",
				o.Username, o.ShareIndex, o.SharePending, claimed)
		}
		return nil
	}
}

// renderShareClaimBegin prints a ShareClaimBeginResponse in the configured
// output format.
func renderShareClaimBegin(resp *transport.ShareClaimBeginResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "NonceHex:   %s\n", resp.NonceHex)
		_, _ = fmt.Fprintf(outputWriter, "ExpiresAt:  %s\n", resp.ExpiresAt.Format("2006-01-02T15:04:05Z"))
		return nil
	}
}

// renderShareClaimComplete prints a ShareClaimCompleteResponse in the
// configured output format.
//
// ShareBytes carries the JSON shareEnvelope the ceremony's claim-complete
// endpoints emit. Table output prints both the traditional hex dump
// (ShareBytes, preserved for backward compatibility) and the raw envelope
// text (Share) so operators can copy the latter straight into a
// share-consuming command; those commands auto-unwrap the JSON envelope
// via resolveShareInput, so no manual extraction of the "value" field is
// required for the round trip to work.
func renderShareClaimComplete(resp *transport.ShareClaimCompleteResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ShareIndex:  %d\n", resp.ShareIndex)
		_, _ = fmt.Fprintf(outputWriter, "ShareBytes:  %x\n", resp.ShareBytes)
		_, _ = fmt.Fprintf(outputWriter, "Share:       %s\n", resp.ShareBytes)
		return nil
	}
}

// ---------------------------------------------------------------------------
// barrier officer (register, list, claim-status, claim-begin, claim-complete)
// ---------------------------------------------------------------------------

// newBarrierOfficerCmd returns the `barrier officer` parent command grouping
// all Security Officer enrollment and share-claim operations.
func newBarrierOfficerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "officer",
		Short: "Manage Security Officers for the system barrier",
		Long: `Security Officer (SO) management commands for the system barrier.

SOs are registered before barrier initialization. After initialization their
shares are sealed to their registered certificates. Each SO claims their share
via a mTLS-authenticated challenge/response flow.`,
	}

	cmd.AddCommand(
		newBarrierOfficerRegisterCmd(),
		newBarrierOfficerListCmd(),
		newBarrierOfficerClaimStatusCmd(),
		newBarrierOfficerClaimBeginCmd(),
		newBarrierOfficerClaimCompleteCmd(),
	)

	return cmd
}

// newBarrierOfficerRegisterCmd returns `barrier officer register`.
func newBarrierOfficerRegisterCmd() *cobra.Command {
	var username, csrFile, authenticatorType, authenticatorRef string

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a Security Officer for the system barrier",
		Long: `Register a Security Officer (SO) for the system barrier.

The officer's CSR is validated, signed by the ceremony CA, and the officer
record is persisted. Share bytes are NOT produced at this step; the barrier
must be initialized (barrier init-shamir) for share distribution to occur.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			csrPEM, err := os.ReadFile(csrFile)
			if err != nil {
				return fmt.Errorf("barrier officer register: reading CSR file: %w", err)
			}
			resp, err := client.Barrier().OfficerRegister(cmd.Context(), &transport.OfficerRegisterRequest{
				Username:          username,
				CSRPEM:            csrPEM,
				AuthenticatorType: authenticatorType,
				AuthenticatorRef:  authenticatorRef,
			})
			if err != nil {
				return err
			}
			return renderOfficerRegisterResponse(resp)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Security Officer username")
	cmd.Flags().StringVar(&csrFile, "csr-file", "", "Path to PEM-encoded CSR file")
	cmd.Flags().StringVar(&authenticatorType, "authenticator-type", "", "Authenticator type (e.g. software, fido2, piv)")
	cmd.Flags().StringVar(&authenticatorRef, "authenticator-ref", "", "Opaque authenticator reference")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("csr-file")

	return cmd
}

// newBarrierOfficerListCmd returns `barrier officer list`.
func newBarrierOfficerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all enrolled Security Officers for the system barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			officers, err := client.Barrier().OfficerList(cmd.Context())
			if err != nil {
				return err
			}
			return renderOfficerStatusList(officers)
		},
	}
}

// newBarrierOfficerClaimStatusCmd returns `barrier officer claim-status`.
func newBarrierOfficerClaimStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claim-status",
		Short: "Show per-officer share claim status for the system barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			officers, err := client.Barrier().OfficerClaimStatus(cmd.Context())
			if err != nil {
				return err
			}
			return renderOfficerClaimStatusList(officers)
		},
	}
}

// newBarrierOfficerClaimBeginCmd returns `barrier officer claim-begin`.
func newBarrierOfficerClaimBeginCmd() *cobra.Command {
	var username string

	cmd := &cobra.Command{
		Use:   "claim-begin",
		Short: "Begin the share-claim handshake for a Security Officer",
		Long: `Issue a challenge nonce to the named Security Officer.

The officer must sign the returned nonce with their registered private key and
submit the signature via 'barrier officer claim-complete' within the expiry window.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Barrier().ShareClaimBegin(cmd.Context(), &transport.ShareClaimBeginRequest{Username: username})
			if err != nil {
				return err
			}
			return renderShareClaimBegin(resp)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Security Officer username")
	_ = cmd.MarkFlagRequired("username")

	return cmd
}

// newBarrierOfficerClaimCompleteCmd returns `barrier officer claim-complete`.
func newBarrierOfficerClaimCompleteCmd() *cobra.Command {
	var username, nonceHex, sigFile string

	cmd := &cobra.Command{
		Use:   "claim-complete",
		Short: "Complete the share-claim handshake and retrieve the share",
		Long: `Verify the officer's nonce signature and return the share bytes exactly once.

The signature file must contain the raw signature bytes produced by signing the
nonce (from claim-begin) with the officer's registered private key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sig, err := os.ReadFile(sigFile)
			if err != nil {
				return fmt.Errorf("barrier officer claim-complete: reading signature file: %w", err)
			}
			resp, err := client.Barrier().ShareClaimComplete(cmd.Context(), &transport.ShareClaimCompleteRequest{
				Username:  username,
				NonceHex:  nonceHex,
				Signature: sig,
			})
			if err != nil {
				return err
			}
			return renderShareClaimComplete(resp)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Security Officer username")
	cmd.Flags().StringVar(&nonceHex, "nonce-hex", "", "Nonce hex from claim-begin")
	cmd.Flags().StringVar(&sigFile, "sig-file", "", "Path to file containing the raw signature bytes")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("nonce-hex")
	_ = cmd.MarkFlagRequired("sig-file")

	return cmd
}

// ---------------------------------------------------------------------------
// barrier recovery officer (register, claim-begin, claim-complete)
// ---------------------------------------------------------------------------

// newBarrierRecoveryOfficerCmd returns the `barrier recovery officer` parent
// command for recovery officer enrollment and share-claim operations.
func newBarrierRecoveryOfficerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "officer",
		Short: "Manage recovery Security Officers for the system barrier",
		Long: `Recovery officer management commands for the system barrier.

Recovery officers receive recovery shares generated by 'barrier recovery generate'.
They are independent from the unseal officers.`,
	}

	cmd.AddCommand(
		newBarrierRecoveryOfficerRegisterCmd(),
		newBarrierRecoveryOfficerClaimBeginCmd(),
		newBarrierRecoveryOfficerClaimCompleteCmd(),
	)

	return cmd
}

// newBarrierRecoveryOfficerRegisterCmd returns `barrier recovery officer register`.
func newBarrierRecoveryOfficerRegisterCmd() *cobra.Command {
	var username, csrFile, authenticatorType, authenticatorRef string

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a recovery Security Officer for the system barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			csrPEM, err := os.ReadFile(csrFile)
			if err != nil {
				return fmt.Errorf("barrier recovery officer register: reading CSR file: %w", err)
			}
			resp, err := client.Barrier().RecoveryOfficerRegister(cmd.Context(), &transport.OfficerRegisterRequest{
				Username:          username,
				CSRPEM:            csrPEM,
				AuthenticatorType: authenticatorType,
				AuthenticatorRef:  authenticatorRef,
			})
			if err != nil {
				return err
			}
			return renderOfficerRegisterResponse(resp)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Recovery officer username")
	cmd.Flags().StringVar(&csrFile, "csr-file", "", "Path to PEM-encoded CSR file")
	cmd.Flags().StringVar(&authenticatorType, "authenticator-type", "", "Authenticator type")
	cmd.Flags().StringVar(&authenticatorRef, "authenticator-ref", "", "Opaque authenticator reference")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("csr-file")

	return cmd
}

// newBarrierRecoveryOfficerClaimBeginCmd returns
// `barrier recovery officer claim-begin`.
func newBarrierRecoveryOfficerClaimBeginCmd() *cobra.Command {
	var username string

	cmd := &cobra.Command{
		Use:   "claim-begin",
		Short: "Begin the recovery share-claim handshake for a recovery officer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Barrier().RecoveryShareClaimBegin(cmd.Context(), &transport.ShareClaimBeginRequest{Username: username})
			if err != nil {
				return err
			}
			return renderShareClaimBegin(resp)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Recovery officer username")
	_ = cmd.MarkFlagRequired("username")

	return cmd
}

// newBarrierRecoveryOfficerClaimCompleteCmd returns
// `barrier recovery officer claim-complete`.
func newBarrierRecoveryOfficerClaimCompleteCmd() *cobra.Command {
	var username, nonceHex, sigFile string

	cmd := &cobra.Command{
		Use:   "claim-complete",
		Short: "Complete the recovery share-claim handshake and retrieve the share",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sig, err := os.ReadFile(sigFile)
			if err != nil {
				return fmt.Errorf("barrier recovery officer claim-complete: reading signature file: %w", err)
			}
			resp, err := client.Barrier().RecoveryShareClaimComplete(cmd.Context(), &transport.ShareClaimCompleteRequest{
				Username:  username,
				NonceHex:  nonceHex,
				Signature: sig,
			})
			if err != nil {
				return err
			}
			return renderShareClaimComplete(resp)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Recovery officer username")
	cmd.Flags().StringVar(&nonceHex, "nonce-hex", "", "Nonce hex from claim-begin")
	cmd.Flags().StringVar(&sigFile, "sig-file", "", "Path to file containing the raw signature bytes")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("nonce-hex")
	_ = cmd.MarkFlagRequired("sig-file")

	return cmd
}

// ---------------------------------------------------------------------------
// tenant barrier-officer (register, list, claim-status, claim-begin,
// claim-complete) + tenant barrier-shamir-seal-vote
//
// Tenant-scoped mirror of the system barrier's Security Officer surface
// (barrier officer / barrier shamir-seal-vote), grafted onto the "tenant"
// command tree from root.go (see newRootCmd) rather than tenant.go, so
// this file alone owns the tenant officer CLI surface.
// ---------------------------------------------------------------------------

// newTenantBarrierOfficerCmd returns the `tenant barrier-officer` parent
// command grouping Security Officer enrollment and share-claim operations
// for a single tenant's dedicated barrier.
func newTenantBarrierOfficerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "barrier-officer",
		Short: "Manage Security Officers for a tenant's dedicated barrier",
		Long: `Security Officer (SO) management commands for a tenant's dedicated barrier.

Mirrors 'barrier officer' (the system barrier's officer surface) scoped to a
single tenant's own barrier via --tenant-id. Applies only to tenants with a
dedicated (barrier/volume) isolation type; a tenant on the shared system
barrier has no officers of its own.`,
	}

	cmd.AddCommand(
		newTenantBarrierOfficerRegisterCmd(),
		newTenantBarrierOfficerListCmd(),
		newTenantBarrierOfficerClaimStatusCmd(),
		newTenantBarrierOfficerClaimBeginCmd(),
		newTenantBarrierOfficerClaimCompleteCmd(),
	)

	return cmd
}

// newTenantBarrierOfficerRegisterCmd returns `tenant barrier-officer register`.
func newTenantBarrierOfficerRegisterCmd() *cobra.Command {
	var tenantID, username, csrFile, authenticatorType, authenticatorRef string

	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a Security Officer for a tenant's dedicated barrier",
		Long: `Register a Security Officer (SO) for a tenant's dedicated barrier.

The officer's CSR is validated, signed by the ceremony CA, and the officer
record is persisted. Share bytes are NOT produced at this step; the tenant
barrier must be initialized with the shamir strategy for share distribution
to occur.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			csrPEM, err := os.ReadFile(csrFile)
			if err != nil {
				return fmt.Errorf("tenant barrier-officer register: reading CSR file: %w", err)
			}
			resp, err := client.Tenants().TenantBarrierOfficerRegister(cmd.Context(), &transport.TenantOfficerRegisterRequest{
				TenantID:          tenantID,
				Username:          username,
				CSRPEM:            csrPEM,
				AuthenticatorType: authenticatorType,
				AuthenticatorRef:  authenticatorRef,
			})
			if err != nil {
				return err
			}
			return renderOfficerRegisterResponse(resp)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	cmd.Flags().StringVar(&username, "username", "", "Security Officer username")
	cmd.Flags().StringVar(&csrFile, "csr-file", "", "Path to PEM-encoded CSR file")
	cmd.Flags().StringVar(&authenticatorType, "authenticator-type", "", "Authenticator type (e.g. software, fido2, piv)")
	cmd.Flags().StringVar(&authenticatorRef, "authenticator-ref", "", "Opaque authenticator reference")
	_ = cmd.MarkFlagRequired("tenant-id")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("csr-file")

	return cmd
}

// newTenantBarrierOfficerListCmd returns `tenant barrier-officer list`.
func newTenantBarrierOfficerListCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all enrolled Security Officers for a tenant's dedicated barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			officers, err := client.Tenants().TenantBarrierOfficerList(cmd.Context(), tenantID)
			if err != nil {
				return err
			}
			return renderOfficerStatusList(officers)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	_ = cmd.MarkFlagRequired("tenant-id")

	return cmd
}

// newTenantBarrierOfficerClaimStatusCmd returns
// `tenant barrier-officer claim-status`.
func newTenantBarrierOfficerClaimStatusCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "claim-status",
		Short: "Show per-officer share claim status for a tenant's dedicated barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			officers, err := client.Tenants().TenantBarrierOfficerClaimStatus(cmd.Context(), tenantID)
			if err != nil {
				return err
			}
			return renderOfficerClaimStatusList(officers)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	_ = cmd.MarkFlagRequired("tenant-id")

	return cmd
}

// newTenantBarrierOfficerClaimBeginCmd returns
// `tenant barrier-officer claim-begin`.
func newTenantBarrierOfficerClaimBeginCmd() *cobra.Command {
	var tenantID, username string

	cmd := &cobra.Command{
		Use:   "claim-begin",
		Short: "Begin the share-claim handshake for a tenant barrier Security Officer",
		Long: `Issue a challenge nonce to the named Security Officer of a tenant's
dedicated barrier.

The officer must sign the returned nonce with their registered private key
and submit the signature via 'tenant barrier-officer claim-complete' within
the expiry window.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Tenants().TenantBarrierShareClaimBegin(cmd.Context(), &transport.TenantShareClaimBeginRequest{
				TenantID: tenantID,
				Username: username,
			})
			if err != nil {
				return err
			}
			return renderShareClaimBegin(resp)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	cmd.Flags().StringVar(&username, "username", "", "Security Officer username")
	_ = cmd.MarkFlagRequired("tenant-id")
	_ = cmd.MarkFlagRequired("username")

	return cmd
}

// newTenantBarrierOfficerClaimCompleteCmd returns
// `tenant barrier-officer claim-complete`.
func newTenantBarrierOfficerClaimCompleteCmd() *cobra.Command {
	var tenantID, username, nonceHex, sigFile string

	cmd := &cobra.Command{
		Use:   "claim-complete",
		Short: "Complete the share-claim handshake and retrieve the tenant barrier share",
		Long: `Verify the officer's nonce signature and return the tenant barrier share
exactly once.

The signature file must contain the raw signature bytes produced by signing
the nonce (from claim-begin) with the officer's registered private key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sig, err := os.ReadFile(sigFile)
			if err != nil {
				return fmt.Errorf("tenant barrier-officer claim-complete: reading signature file: %w", err)
			}
			resp, err := client.Tenants().TenantBarrierShareClaimComplete(cmd.Context(), &transport.TenantShareClaimCompleteRequest{
				TenantID:  tenantID,
				Username:  username,
				NonceHex:  nonceHex,
				Signature: sig,
			})
			if err != nil {
				return err
			}
			return renderShareClaimComplete(resp)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	cmd.Flags().StringVar(&username, "username", "", "Security Officer username")
	cmd.Flags().StringVar(&nonceHex, "nonce-hex", "", "Nonce hex from claim-begin")
	cmd.Flags().StringVar(&sigFile, "sig-file", "", "Path to file containing the raw signature bytes")
	_ = cmd.MarkFlagRequired("tenant-id")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("nonce-hex")
	_ = cmd.MarkFlagRequired("sig-file")

	return cmd
}

// newTenantBarrierShamirSealVoteCmd returns the
// `tenant barrier-shamir-seal-vote` subcommand. Each enrolled tenant SO
// submits their share as a vote to seal the tenant's dedicated barrier;
// once the quorum threshold is met the barrier seals automatically.
func newTenantBarrierShamirSealVoteCmd() *cobra.Command {
	var tenantID, share string

	cmd := &cobra.Command{
		Use:   "barrier-shamir-seal-vote",
		Short: "Submit a Shamir share as a seal vote for a tenant's dedicated barrier",
		Long: `Submit a Shamir share as a quorum vote to seal a tenant's dedicated
barrier (Shamir-strategy tenant barriers only).

When the quorum threshold is met all submitted shares are wiped and the
tenant barrier transitions to the sealed state automatically.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveShareInput(share)
			if err != nil {
				return err
			}
			resp, err := client.Tenants().TenantBarrierShamirSealVote(cmd.Context(), &transport.TenantShamirSealVoteRequest{
				TenantID: tenantID,
				Share:    resolved,
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
				_, _ = fmt.Fprintf(outputWriter, "Required:   %d\n", resp.Required)
				_, _ = fmt.Fprintf(outputWriter, "Submitted:  %d\n", resp.Submitted)
				_, _ = fmt.Fprintf(outputWriter, "Complete:   %v\n", resp.Complete)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	cmd.Flags().StringVar(&share, "share", "", "Shamir share to submit as a seal vote")
	_ = cmd.MarkFlagRequired("tenant-id")
	_ = cmd.MarkFlagRequired("share")

	return cmd
}
