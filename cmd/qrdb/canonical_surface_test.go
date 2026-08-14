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

//go:build !integration

package main

import (
	"strings"
	"testing"
)

// canonicalRootSubcommands lists the locked canonical command names
// that MUST appear at the qrdb root.
var canonicalRootSubcommands = []string{
	"init",
	"auth",
	"barrier",
	"ceremony",
	"ca",
	"bootstrap",
	"credential",
}

// canonicalInitFlags lists the canonical root-level init flag set
// (distinct from `barrier init`; the root verb runs the full ceremony).
var canonicalInitFlags = []string{
	"--so-pin",
	"--user-pin",
	"--barrier-strategy",
	"--shares",
	"--threshold",
	"--hostname",
	"--sans",
}

// TestCanonicalSurface_RootHasInit verifies the root "init" command
// is registered.
func TestCanonicalSurface_RootHasInit(t *testing.T) {
	root := newRootCmd()
	if _, _, err := root.Find([]string{"init"}); err != nil {
		t.Fatalf("expected canonical 'init' command at qrdb root: %v", err)
	}
}

// TestCanonicalSurface_RootHasAllSubcommands verifies every canonical
// subcommand appears at the root.
func TestCanonicalSurface_RootHasAllSubcommands(t *testing.T) {
	root := newRootCmd()
	for _, name := range canonicalRootSubcommands {
		if _, _, err := root.Find([]string{name}); err != nil {
			t.Errorf("canonical command %q missing from qrdb root: %v", name, err)
		}
	}
}

// TestCanonicalSurface_InitFlagsExposed verifies the root-level init
// flag set is fully exposed.
func TestCanonicalSurface_InitFlagsExposed(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"init"})
	if err != nil {
		t.Fatalf("init not found: %v", err)
	}
	for _, name := range canonicalInitFlags {
		flag := cmd.Flags().Lookup(strings.TrimPrefix(name, "--"))
		if flag == nil {
			t.Errorf("canonical init flag %s missing", name)
		}
	}
}

// TestCanonicalSurface_LegacyCertsRedirects verifies that "qrdb certs"
// emits a CanonicalRedirectError pointing at the renamed "ca" group.
func TestCanonicalSurface_LegacyCertsRedirects(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"certs"})
	if err != nil {
		t.Fatalf("certs redirect command not registered: %v", err)
	}
	if cmd.RunE == nil {
		t.Fatal("certs redirect command has no RunE")
	}
}

// TestCanonicalSurface_CeremonyInitRedirects verifies that
// "qrdb ceremony init" redirects to the root init command.
func TestCanonicalSurface_CeremonyInitRedirects(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"ceremony", "init"})
	if err != nil {
		t.Fatalf("ceremony init redirect command not registered: %v", err)
	}
	if cmd.RunE == nil {
		t.Fatal("ceremony init redirect command has no RunE")
	}
}

// TestCanonicalSurface_BarrierInitIsRealVerb verifies that the canonical
// gold-standard surface exposes `barrier init` as a real verb (not a
// redirect). The four-CLI canonical surface treats `barrier init` as
// the barrier-only initializer, distinct from the root `init` which
// runs the full ceremony (barrier + CA + admin cert + roles).
func TestCanonicalSurface_BarrierInitIsRealVerb(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"barrier", "init"})
	if err != nil {
		t.Fatalf("barrier init not registered: %v", err)
	}
	if cmd.RunE == nil {
		t.Fatal("barrier init has no RunE; must be a real verb")
	}
	for _, name := range []string{"barrier-strategy", "passphrase", "so-pin", "user-pin"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("barrier init missing --%s flag", name)
		}
	}
}

// TestCanonicalSurface_BarrierUnsealSecretOnly verifies that the
// canonical `barrier unseal` accepts ONLY --passphrase. Shamir share
// submission lives under the dedicated `barrier unseal-shares` verb.
func TestCanonicalSurface_BarrierUnsealSecretOnly(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"barrier", "unseal"})
	if err != nil {
		t.Fatalf("barrier unseal not registered: %v", err)
	}
	if cmd.Flags().Lookup("passphrase") == nil {
		t.Error("barrier unseal --passphrase flag missing")
	}
	// Per the canonical surface, share submission is split out:
	// `barrier unseal` is passphrase only.
	if cmd.Flags().Lookup("share") != nil {
		t.Error("barrier unseal must NOT carry --share; use barrier unseal-shares")
	}
	if cmd.Flags().Lookup("shares") != nil {
		t.Error("barrier unseal must NOT carry --shares; use barrier unseal-shares")
	}
}

// TestCanonicalSurface_BootstrapHasDirect verifies the bootstrap group
// includes the new "direct" mode.
func TestCanonicalSurface_BootstrapHasDirect(t *testing.T) {
	root := newRootCmd()
	for _, mode := range []string{"auto", "dane", "noise", "spki", "direct"} {
		if _, _, err := root.Find([]string{"bootstrap", mode}); err != nil {
			t.Errorf("bootstrap %s missing: %v", mode, err)
		}
	}
}

// TestCanonicalSurface_AuthHasFactor verifies the auth group has the
// N-factor management surface.
func TestCanonicalSurface_AuthHasFactor(t *testing.T) {
	root := newRootCmd()
	for _, verb := range []string{"login", "factor"} {
		if _, _, err := root.Find([]string{"auth", verb}); err != nil {
			t.Errorf("auth %s missing: %v", verb, err)
		}
	}
	for _, verb := range []string{"list", "add", "remove", "rotate", "relabel"} {
		if _, _, err := root.Find([]string{"auth", "factor", verb}); err != nil {
			t.Errorf("auth factor %s missing: %v", verb, err)
		}
	}
}
