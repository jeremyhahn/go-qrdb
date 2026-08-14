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

// TestCAInfoCmd_HelpDoesNotOverpromise guards against re-introducing the
// misleading "Show CA metadata (DN, fingerprint, validity)" description:
// "ca info" is currently an alias for "ca list" (it renders the
// issued-certificate table), because standalone root-CA introspection is
// not yet exposed through the SDK CertClient contract on any transport
// (see the newCACmd doc comment in ca.go). Its help text must say so.
func TestCAInfoCmd_HelpDoesNotOverpromise(t *testing.T) {
	cmd := newCAInfoCmd()
	if strings.Contains(cmd.Short, "DN, fingerprint, validity") {
		t.Errorf("ca info Short must not advertise standalone CA metadata it does not deliver, got: %q", cmd.Short)
	}
	if !strings.Contains(cmd.Short, "alias") {
		t.Errorf("ca info Short should document that it is an alias for \"list\", got: %q", cmd.Short)
	}
	if cmd.Use != "info" {
		t.Errorf("expected Use=info, got %q", cmd.Use)
	}
}

// TestCABundleCmd_HelpDoesNotOverpromise guards against re-introducing the
// misleading "Print the active CA bundle (CA + chain)" description: "ca
// bundle" is currently an alias for "ca list" (it renders the
// issued-certificate table), because a standalone CA+chain bundle export
// is not yet exposed through the SDK CertClient contract on any transport
// (see the newCACmd doc comment in ca.go). Its help text must say so.
func TestCABundleCmd_HelpDoesNotOverpromise(t *testing.T) {
	cmd := newCABundleCmd()
	if strings.Contains(cmd.Short, "CA + chain") {
		t.Errorf("ca bundle Short must not advertise a standalone CA+chain bundle it does not deliver, got: %q", cmd.Short)
	}
	if !strings.Contains(cmd.Short, "alias") {
		t.Errorf("ca bundle Short should document that it is an alias for \"list\", got: %q", cmd.Short)
	}
	if cmd.Use != "bundle" {
		t.Errorf("expected Use=bundle, got %q", cmd.Use)
	}
}

// TestNewCACmd_GroupHelpDoesNotOverpromise guards against re-introducing
// the misleading group-level Long description for the "ca" command group.
func TestNewCACmd_GroupHelpDoesNotOverpromise(t *testing.T) {
	cmd := newCACmd()
	if strings.Contains(cmd.Long, "DN, fingerprint, validity") {
		t.Errorf("ca group Long must not advertise standalone CA metadata info does not deliver, got: %q", cmd.Long)
	}
	if strings.Contains(cmd.Long, "CA + chain") {
		t.Errorf("ca group Long must not advertise a standalone CA+chain bundle export, got: %q", cmd.Long)
	}
}
