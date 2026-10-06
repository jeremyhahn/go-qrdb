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

package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// parseTenantOfficerFlags unit tests
// ---------------------------------------------------------------------------

func TestParseTenantOfficerFlags_Empty(t *testing.T) {
	officers, err := parseTenantOfficerFlags("--officers", transport.RoleSecurityOfficer, nil)
	if err != nil {
		t.Fatalf("parseTenantOfficerFlags(nil): %v", err)
	}
	if officers != nil {
		t.Errorf("officers = %+v, want nil", officers)
	}
}

func TestParseTenantOfficerFlags_Success(t *testing.T) {
	_, aliceCSR, err := newClientKeyAndCSR("alice")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR(alice): %v", err)
	}
	_, bobCSR, err := newClientKeyAndCSR("bob")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR(bob): %v", err)
	}
	aliceCSRPath := writeTempFile(t, "alice-csr.pem", aliceCSR)
	bobCSRPath := writeTempFile(t, "bob-csr.pem", bobCSR)

	officers, err := parseTenantOfficerFlags("--officers", transport.RoleSecurityOfficer,
		[]string{"alice=" + aliceCSRPath, "bob=" + bobCSRPath})
	if err != nil {
		t.Fatalf("parseTenantOfficerFlags: %v", err)
	}
	if len(officers) != 2 {
		t.Fatalf("len(officers) = %d, want 2", len(officers))
	}
	if officers[0].Username != "alice" || officers[0].Role != transport.RoleSecurityOfficer || officers[0].CSRPEM != string(aliceCSR) {
		t.Errorf("officers[0] = %+v", officers[0])
	}
	if officers[1].Username != "bob" || officers[1].Role != transport.RoleSecurityOfficer || officers[1].CSRPEM != string(bobCSR) {
		t.Errorf("officers[1] = %+v", officers[1])
	}
}

func TestParseTenantOfficerFlags_MalformedEntry(t *testing.T) {
	_, err := parseTenantOfficerFlags("--officers", transport.RoleSecurityOfficer, []string{"no-equals-sign"})
	var invalidArg *InvalidArgError
	if !errors.As(err, &invalidArg) {
		t.Fatalf("parseTenantOfficerFlags(malformed) = %v, want *InvalidArgError", err)
	}
	if invalidArg.Arg != "--officers" {
		t.Errorf("InvalidArgError.Arg = %q, want --officers", invalidArg.Arg)
	}
}

func TestParseTenantOfficerFlags_MissingUsername(t *testing.T) {
	_, err := parseTenantOfficerFlags("--custodians", transport.RoleCustodian, []string{"=path/to/csr.pem"})
	var invalidArg *InvalidArgError
	if !errors.As(err, &invalidArg) {
		t.Fatalf("parseTenantOfficerFlags(missing username) = %v, want *InvalidArgError", err)
	}
}

func TestParseTenantOfficerFlags_MissingCSRPath(t *testing.T) {
	_, err := parseTenantOfficerFlags("--custodians", transport.RoleCustodian, []string{"alice="})
	var invalidArg *InvalidArgError
	if !errors.As(err, &invalidArg) {
		t.Fatalf("parseTenantOfficerFlags(missing csr path) = %v, want *InvalidArgError", err)
	}
}

func TestParseTenantOfficerFlags_FileReadError(t *testing.T) {
	_, err := parseTenantOfficerFlags("--officers", transport.RoleSecurityOfficer,
		[]string{"alice=" + filepath.Join(t.TempDir(), "does-not-exist.pem")})
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Fatalf("parseTenantOfficerFlags(missing file) = %v, want *FileReadError", err)
	}
}

// ---------------------------------------------------------------------------
// printTenantBarrierInitResponse unit tests
// ---------------------------------------------------------------------------

// executePrintTenantBarrierInitResponse saves/restores outputFormat and
// outputWriter around a call to printTenantBarrierInitResponse, returning
// captured output.
func executePrintTenantBarrierInitResponse(t *testing.T, format string, resp *transport.TenantBarrierInitResponse) (string, error) {
	t.Helper()
	oldFormat := outputFormat
	oldWriter := outputWriter
	t.Cleanup(func() {
		outputFormat = oldFormat
		outputWriter = oldWriter
	})
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	err := printTenantBarrierInitResponse(resp)
	return buf.String(), err
}

func TestPrintTenantBarrierInitResponse_Nil(t *testing.T) {
	out, err := executePrintTenantBarrierInitResponse(t, "table", nil)
	if err != nil {
		t.Fatalf("printTenantBarrierInitResponse(nil): %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("output = %q, want OK", out)
	}
}

func TestPrintTenantBarrierInitResponse_ZeroShareCount(t *testing.T) {
	out, err := executePrintTenantBarrierInitResponse(t, "table", &transport.TenantBarrierInitResponse{
		TenantID: "tenant-1", Strategy: "passphrase",
	})
	if err != nil {
		t.Fatalf("printTenantBarrierInitResponse(ShareCount=0): %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("output = %q, want OK", out)
	}
}

func TestPrintTenantBarrierInitResponse_Table(t *testing.T) {
	resp := &transport.TenantBarrierInitResponse{
		TenantID:    "tenant-1",
		Strategy:    "shamir",
		Threshold:   2,
		TotalShares: 3,
		ShareCount:  3,
		Holders: []transport.TenantBarrierHolderCert{
			{Username: "alice", Role: transport.RoleSecurityOfficer},
			{Username: "bob", Role: transport.RoleCustodian},
		},
	}
	out, err := executePrintTenantBarrierInitResponse(t, "table", resp)
	if err != nil {
		t.Fatalf("printTenantBarrierInitResponse: %v", err)
	}
	for _, want := range []string{
		"TenantID:    tenant-1",
		"Strategy:    shamir",
		"Threshold:   2",
		"TotalShares: 3",
		"ShareCount:  3",
		"Holder:      alice (so)",
		"Holder:      bob (custodian)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got: %q", want, out)
		}
	}
}

func TestPrintTenantBarrierInitResponse_JSON(t *testing.T) {
	resp := &transport.TenantBarrierInitResponse{TenantID: "tenant-1", Strategy: "shamir", ShareCount: 1}
	out, err := executePrintTenantBarrierInitResponse(t, "json", resp)
	if err != nil {
		t.Fatalf("printTenantBarrierInitResponse json: %v", err)
	}
	if !strings.Contains(out, `"tenant_id"`) {
		t.Errorf("output = %q, want JSON with tenant_id", out)
	}
}

func TestPrintTenantBarrierInitResponse_YAML(t *testing.T) {
	resp := &transport.TenantBarrierInitResponse{TenantID: "tenant-1", Strategy: "shamir", ShareCount: 1}
	out, err := executePrintTenantBarrierInitResponse(t, "yaml", resp)
	if err != nil {
		t.Fatalf("printTenantBarrierInitResponse yaml: %v", err)
	}
	if !strings.Contains(out, "tenantid: tenant-1") {
		t.Errorf("output = %q, want YAML with tenantid", out)
	}
}

// ---------------------------------------------------------------------------
// "tenant barrier-init" CLI-level tests for --officers/--custodians
// ---------------------------------------------------------------------------

func TestTenantBarrierInitCmd_OfficersMalformedEntry(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	_, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001",
		"--strategy", "shamir", "--shares", "1", "--officers", "bad-entry-no-equals")
	var invalidArg *InvalidArgError
	if !errors.As(err, &invalidArg) {
		t.Fatalf("barrier-init --officers <malformed> = %v, want *InvalidArgError", err)
	}
	if svc.captured != nil {
		t.Fatal("service should not be called when --officers parsing fails")
	}
}

func TestTenantBarrierInitCmd_OfficersFileReadError(t *testing.T) {
	svc := newStrategyCapturingTenantService()
	_, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001",
		"--strategy", "shamir", "--shares", "1",
		"--officers", "alice="+filepath.Join(t.TempDir(), "missing.pem"))
	var fileErr *FileReadError
	if !errors.As(err, &fileErr) {
		t.Fatalf("barrier-init --officers <missing file> = %v, want *FileReadError", err)
	}
}

// TestTenantBarrierInitCmd_SharesMismatch verifies the combined officer+
// custodian roster count is validated against --shares before the service is
// ever called.
func TestTenantBarrierInitCmd_SharesMismatch(t *testing.T) {
	_, csrPEM, err := newClientKeyAndCSR("alice")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR: %v", err)
	}
	csrPath := writeTempFile(t, "alice-csr.pem", csrPEM)

	svc := newStrategyCapturingTenantService()
	_, err = executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001",
		"--strategy", "shamir", "--shares", "2",
		"--officers", "alice="+csrPath)
	var invalidArg *InvalidArgError
	if !errors.As(err, &invalidArg) {
		t.Fatalf("barrier-init --shares mismatch = %v, want *InvalidArgError", err)
	}
	if invalidArg.Arg != "--shares" {
		t.Errorf("InvalidArgError.Arg = %q, want --shares", invalidArg.Arg)
	}
	if svc.captured != nil {
		t.Fatal("service should not be called when the roster count mismatches --shares")
	}
}

// TestTenantBarrierInitCmd_OfficersAndCustodiansRosterSuccess verifies a
// roster whose combined --officers/--custodians count equals --shares is
// accepted and the call reaches the service, rendering the Shamir share
// metadata in table output.
func TestTenantBarrierInitCmd_OfficersAndCustodiansRosterSuccess(t *testing.T) {
	_, officerCSR, err := newClientKeyAndCSR("alice")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR(alice): %v", err)
	}
	_, custodianCSR, err := newClientKeyAndCSR("bob")
	if err != nil {
		t.Fatalf("newClientKeyAndCSR(bob): %v", err)
	}
	officerPath := writeTempFile(t, "alice-csr.pem", officerCSR)
	custodianPath := writeTempFile(t, "bob-csr.pem", custodianCSR)

	svc := newStrategyCapturingTenantService()
	out, err := executeTenantCmd(t, svc, "barrier-init", "--id", "tenant-001",
		"--strategy", "shamir", "--shares", "2", "--threshold", "2",
		"--officers", "alice="+officerPath,
		"--custodians", "bob="+custodianPath,
	)
	if err != nil {
		t.Fatalf("barrier-init with officers+custodians roster: %v", err)
	}
	if svc.captured == nil {
		t.Fatal("expected TenantBarrierInitWithStrategy to be called")
	}
	if !strings.Contains(out, "ShareCount:  2") {
		t.Errorf("output missing ShareCount: 2, got: %q", out)
	}
}
