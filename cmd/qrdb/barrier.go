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
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// barrierStrategyInit enumerates the strategies accepted by `barrier
// init`. The root-level `init` ceremony accepts the full strategy set
// (pkcs11, tpm2, awskms, gcpkms, azurekv, vault) because it provisions
// the CA and PIN-store in addition to the barrier; the dedicated
// `barrier init` verb is limited to the credential strategies. Shamir is
// not one: `barrier init-shamir` initializes it over the enrolled holders.
var barrierStrategyInit = map[string]struct{}{
	"none":       {},
	"passphrase": {},
}

// newBarrierCmd returns the canonical barrier command group.
func newBarrierCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "barrier",
		Short: "Cryptographic barrier management (canonical CLI surface)",
		Long: `Canonical barrier command group.

The verb tree exposed here is the gold-standard surface shared by every
CLI binary in the stack (qrdb, xkmsctl, quicbaasctl, platformctl):

  status         Show barrier state
  init           Initialize barrier with passphrase or none
  init-shamir    Initialize Shamir custody over the enrolled share holders
  unseal         Unseal using a passphrase
  seal           Seal the barrier
  officer        Enrol, list, claim and revoke share holders
  quorum         Submit an authenticated holder's share to a quorum round
                 (unseal, seal, root-token, rekey, recover)
  rekey          Open a quorum-gated rekey round
  recovery       Generate the independent recovery key`,
	}

	cmd.AddCommand(
		newBarrierStatusCmd(),
		newBarrierInitCmd(),
		newBarrierInitShamirCmd(),
		newBarrierUnsealCmd(),
		newBarrierSealCmd(),
		newBarrierClusterUnsealCmd(),
		newHolderRekeyCmd("rekey", holderSystem),
		newBarrierEnableEncryptionCmd(),
		newBarrierRecoveryCmd(),
		newHolderOfficerCmd("officer", "Manage the system barrier's share holders", holderSystem),
		newHolderQuorumCmd("quorum", holderSystem),
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
		printClusterSeal(resp.ClusterSeal)
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
// It refuses the SYSTEM barrier of a fresh node with a typed error naming
// `qrdb init` (go-qrdb#548): that barrier is brought up by `qrdb init`,
// which runs the full ceremony (barrier + CA + admin cert + roles) in the
// sealed window, in one call, before anything else is unsealed. This verb
// has always been the SYSTEM barrier only -- POST /v1/barrier/initialize
// carries no tenant or database scope -- so it is now refused
// unconditionally; it does not gain a refusal exception for some other
// scope it never had. A tenant admin initializes their own tenant barrier
// with the separate "qrdb tenant barrier-init" command, untouched by this
// change; there is no equivalent per-database command.
func newBarrierInitCmd() *cobra.Command {
	f := &barrierInitFlags{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the system barrier (refused: use qrdb init instead)",
		Long: `Initialize the system cryptographic barrier.

Refused unconditionally (go-qrdb#548): the system barrier is brought up by
"qrdb init", the only system bootstrap, which also runs the ceremony and
issues the bootstrap admin's credential in the same call. This command has
always been the system barrier only; it carries no tenant or database
scope, so there is no case in which it still succeeds. A tenant admin
initializes their own tenant barrier with "qrdb tenant barrier-init"
instead, a separate command untouched by this change.

Strategies (documented for the error message's context only; every
invocation is refused regardless of which one is given):
  none        Identity passthrough; data stored in cleartext (dev only).
  passphrase  Single passphrase-derived master key (default).

Shamir M-of-N custody is initialized over the enrolled share holders with
'barrier init-shamir', never from a passphrase.

--passphrase is required when --barrier-strategy is passphrase and
rejected when --barrier-strategy is none. PINs are optional but
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
				Secret:          secretBytes(f.passphrase),
				BarrierStrategy: f.strategy,
				SOPin:           f.soPin,
				UserPin:         f.userPin,
			}
			defer secure.Wipe(req.Secret)
			if err := client.Barrier().BarrierInitialize(cmd.Context(), req); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&f.strategy, "barrier-strategy", "passphrase",
		"Barrier strategy: none | passphrase (use init-shamir for shamir)")
	cmd.Flags().StringVar(&f.passphrase, "passphrase", "",
		"Master passphrase (required for passphrase)")
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
	if f.strategy == "shamir" {
		return &InvalidArgError{
			Arg:    "--barrier-strategy",
			Reason: "a shamir barrier is initialized over its enrolled share holders with 'barrier init-shamir'",
		}
	}
	if _, ok := barrierStrategyInit[f.strategy]; !ok {
		return &InvalidArgError{
			Arg:    "--barrier-strategy",
			Reason: "must be one of none, passphrase",
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
	threshold int
	total     int
	policy    string
	soPin     string
	userPin   string
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
		Long: `Initialize the barrier under Shamir M-of-N custody.

The server generates the root key and an independent unseal key, and splits
the unseal key --total ways (which must equal the number of holders enrolled
with 'barrier officer register'), --threshold of which unseal. PIN flags are
optional but follow the same dual-control rule as 'barrier init' --
supplying --so-pin requires --user-pin.

The response is share-free: one HPKE-sealed envelope is held for each
holder, who claims it once with 'barrier officer claim'.

Example:
  qrdb barrier init-shamir --threshold 3 --total 5`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateBarrierInitShamirFlags(f); err != nil {
				return err
			}
			req := &transport.BarrierInitializeShamirRequest{
				Threshold:   f.threshold,
				TotalShares: f.total,
				Policy:      f.policy,
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

	cmd.Flags().IntVar(&f.threshold, "threshold", 0, "Minimum holders required to unseal (M)")
	cmd.Flags().IntVar(&f.total, "total", 0, "Number of shares (N, the enrolled holder count)")
	cmd.Flags().StringVar(&f.policy, "policy", "", "Factor policy: designated (default) or any-enrolled")
	cmd.Flags().StringVar(&f.soPin, "so-pin", "", "Security Officer PIN (optional; requires --user-pin)")
	cmd.Flags().StringVar(&f.userPin, "user-pin", "", "User PIN (optional; requires --so-pin)")
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
		_, _ = fmt.Fprintln(outputWriter, "One sealed envelope is held for each enrolled holder.")
		_, _ = fmt.Fprintln(outputWriter, "Each holder claims its envelope once via 'barrier officer claim'.")
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
			req := &transport.BarrierUnsealRequest{Secret: secretBytes(passphrase)}
			defer secure.Wipe(req.Secret)
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
			// BarrierSeal takes no request of its own: every network
			// transport reads the SO PIN from the context
			// (transport.WithSOPIN) and sends it in the request's so_pin
			// field and the transport's PIN header or metadata. An empty
			// --so-pin binds nothing.
			ctx, release, err := transport.WithSOPIN(cmd.Context(), []byte(f.soPin))
			if err != nil {
				return err
			}
			defer release()
			if err := client.Barrier().BarrierSeal(ctx); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (required once an SO PIN is provisioned)")
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
				Passphrase:      secretBytes(f.passphrase),
				ShamirThreshold: f.threshold,
				ShamirTotal:     f.total,
			}
			defer secure.Wipe(req.Passphrase)
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

The recovery key is an unseal key independent of the unseal domain's, split
to the holders enrolled in the recovery domain ('barrier officer register
--domain recovery'). Recovery is a holder quorum: 'barrier quorum submit
--domain recovery --op recover'.

Canonical surface:
  recovery generate   Generate and distribute the recovery key (M-of-N)

Operational verbs (not part of the canonical surface):
  recovery delete     Delete all stored recovery keys
  recovery status     Show whether recovery keys are present`,
	}

	cmd.AddCommand(
		newBarrierRecoveryGenerateCmd(),
		newBarrierRecoveryDeleteCmd(),
		newBarrierRecoveryStatusCmd(),
	)

	return cmd
}

// barrierRecoveryGenerateFlags holds the flag set for `barrier recovery
// generate`.
type barrierRecoveryGenerateFlags struct {
	threshold int
	policy    string
	soPin     string
}

// newBarrierRecoveryGenerateCmd returns the canonical
// `barrier recovery generate` subcommand.
func newBarrierRecoveryGenerateCmd() *cobra.Command {
	f := &barrierRecoveryGenerateFlags{}

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate M-of-N recovery keys",
		Long: `Generate the recovery key.

The recovery key is an unseal key independent of the unseal domain's, with
its own wrapped root-key blob. It is split --threshold-of-N to the N holders
enrolled in the recovery domain, each of whom claims a sealed envelope.
Generation requires the Security Officer PIN.

Example:
  qrdb barrier recovery generate --threshold 3 --so-pin SO123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.threshold < 2 {
				return &InvalidArgError{Arg: "--threshold", Reason: "must be >= 2"}
			}
			req := &transport.BarrierGenerateRecoveryKeysRequest{
				Threshold: f.threshold,
				Policy:    f.policy,
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
	cmd.Flags().StringVar(&f.policy, "policy", "", "Factor policy: designated (default) or any-enrolled")
	cmd.Flags().StringVar(&f.soPin, "so-pin", "",
		"Security Officer PIN (required once an SO PIN is provisioned)")
	_ = cmd.MarkFlagRequired("threshold")

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
		_, _ = fmt.Fprintln(outputWriter, "One sealed envelope is held for each recovery holder.")
		_, _ = fmt.Fprintln(outputWriter, "Each holder claims it once via 'barrier officer claim --domain recovery'.")
		return nil
	}
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
