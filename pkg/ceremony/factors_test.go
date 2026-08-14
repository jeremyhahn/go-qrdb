// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//     See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package ceremony

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// stubFactor implements authfactors.Factor plus the optional
// ceremony.FactorAvailability probe, so a test can drive preflight without
// real hardware. enrolled records whether Enroll ran, proving the preflight
// refused before anything changed.
type stubFactor struct {
	name          string
	available     bool
	reason        string
	probes        bool
	enrolled      bool
	enrollErr     error
	captureParams func(map[string]any)
}

func (s *stubFactor) Name() string { return s.name }

func (s *stubFactor) Enroll(_ context.Context, params *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	s.enrolled = true
	if s.captureParams != nil && params != nil {
		s.captureParams(params.MethodParams)
	}
	if s.enrollErr != nil {
		return nil, s.enrollErr
	}
	return &authfactors.Enrollment{FactorName: s.name}, nil
}

func (s *stubFactor) Challenge(context.Context, *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{}, nil
}

func (s *stubFactor) Verify(context.Context, *authfactors.Challenge, *authfactors.Response) error {
	return nil
}

func (s *stubFactor) Available(context.Context) (bool, string) {
	return s.available, s.reason
}

// plainFactor implements authfactors.Factor WITHOUT the availability probe,
// which must be treated as always available.
type plainFactor struct{ name string }

func (p *plainFactor) Name() string { return p.name }

func (p *plainFactor) Enroll(context.Context, *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{FactorName: p.name}, nil
}

func (p *plainFactor) Challenge(context.Context, *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{}, nil
}

func (p *plainFactor) Verify(context.Context, *authfactors.Challenge, *authfactors.Response) error {
	return nil
}

func registryWith(t *testing.T, factors ...authfactors.Factor) *authfactors.FactorRegistry {
	t.Helper()
	reg := authfactors.NewFactorRegistry()
	for _, f := range factors {
		if err := reg.Register(f); err != nil {
			t.Fatalf("Register %s: %v", f.Name(), err)
		}
	}
	return reg
}

func TestAdminFactor_SharesTheLoginVocabulary(t *testing.T) {
	// Enrollment and login must use one vocabulary: the string an operator
	// types at init is the string stored on the user record. Drift here means
	// an admin enrolls a factor they then cannot authenticate with.
	if string(FactorMTLS) != "mtls" {
		t.Errorf("FactorMTLS = %q, want mtls", FactorMTLS)
	}
	if string(FactorFIDO2) != "fido2" {
		t.Errorf("FactorFIDO2 = %q, want fido2", FactorFIDO2)
	}
	if string(FactorPIV) != "piv" {
		t.Errorf("FactorPIV = %q, want piv", FactorPIV)
	}
	for _, f := range EnrollableAdminFactors() {
		if !f.IsValid() {
			t.Errorf("%q is enrollable but not a valid login factor", f)
		}
		if !IsEnrollableAdminFactor(f) {
			t.Errorf("EnrollableAdminFactors() lists %q but the predicate rejects it", f)
		}
	}
}

func TestAdminFactor_EnrollableSetIsNarrowerThanLogin(t *testing.T) {
	// PKCS#11 is a valid LOGIN factor but is not bootstrappable at init: an
	// HSM-held admin credential is enrolled later, against a configured
	// backend, once the system is running.
	if !auth.AuthFactorPKCS11.IsValid() {
		t.Fatal("pkcs11 should be a valid login factor")
	}
	if IsEnrollableAdminFactor(auth.AuthFactorPKCS11) {
		t.Error("pkcs11 must not be enrollable during the init ceremony")
	}
	// Nothing password-shaped is admissible anywhere in this stack.
	for _, bad := range []AdminFactor{"password", "totp", "token", "", "cert"} {
		if IsEnrollableAdminFactor(bad) {
			t.Errorf("factor %q must not be enrollable", bad)
		}
	}
}

func TestNewEnrollmentPlan_DefaultsToCertificate(t *testing.T) {
	plan, err := NewEnrollmentPlan(nil)
	if err != nil {
		t.Fatalf("NewEnrollmentPlan(nil): %v", err)
	}
	got := plan.Factors()
	if len(got) != 1 || got[0] != FactorMTLS {
		t.Fatalf("default plan = %v, want [cert]", got)
	}
}

func TestNewEnrollmentPlan_CertificateEnrollsFirst(t *testing.T) {
	// The certificate is the credential that still works if a hardware
	// ceremony is interrupted, so it must not be ordered behind one.
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorFIDO2, FactorMTLS, FactorPIV})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	got := plan.Factors()
	if got[0] != FactorMTLS {
		t.Fatalf("plan order = %v, want cert first", got)
	}
	if len(got) != 3 {
		t.Fatalf("plan = %v, want 3 factors", got)
	}
}

func TestNewEnrollmentPlan_PreservesOrderWithoutCertificate(t *testing.T) {
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorPIV, FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	got := plan.Factors()
	if got[0] != FactorPIV || got[1] != FactorFIDO2 {
		t.Fatalf("plan = %v, want [piv passkey]", got)
	}
}

func TestNewEnrollmentPlan_CollapsesDuplicates(t *testing.T) {
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorFIDO2, FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	if got := plan.Factors(); len(got) != 1 {
		t.Fatalf("plan = %v, want one factor", got)
	}
}

func TestNewEnrollmentPlan_RejectsUnknownFactor(t *testing.T) {
	_, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS, "password"})
	var unsupported *UnsupportedFactorError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *UnsupportedFactorError", err)
	}
	if unsupported.Factor != "password" {
		t.Errorf("Factor = %q, want password", unsupported.Factor)
	}
}

func TestEnrollmentPlan_PreflightRefusesUnregisteredFactor(t *testing.T) {
	reg := registryWith(t, &stubFactor{name: string(FactorMTLS), available: true})
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS, FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	var unsupported *UnsupportedFactorError
	if err := plan.Preflight(context.Background(), reg); !errors.As(err, &unsupported) {
		t.Fatalf("Preflight = %v, want *UnsupportedFactorError", err)
	}
}

func TestEnrollmentPlan_PreflightRefusesBeforeEnrolling(t *testing.T) {
	// The whole point of preflight: a missing authenticator must fail the
	// ceremony BEFORE the barrier is initialized, otherwise the operator is
	// left with an initialized store and a half-enrolled admin.
	certFactor := &stubFactor{name: string(FactorMTLS), available: true}
	passkeyFactor := &stubFactor{
		name:      string(FactorFIDO2),
		available: false,
		reason:    "no FIDO2 authenticator detected",
	}
	reg := registryWith(t, certFactor, passkeyFactor)

	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS, FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	var unavailable *FactorUnavailableError
	if err := plan.Preflight(context.Background(), reg); !errors.As(err, &unavailable) {
		t.Fatalf("Preflight = %v, want *FactorUnavailableError", err)
	}
	if unavailable.Reason != "no FIDO2 authenticator detected" {
		t.Errorf("Reason = %q, want the enroller's reason", unavailable.Reason)
	}
	if certFactor.enrolled || passkeyFactor.enrolled {
		t.Error("preflight must not enroll anything")
	}
}

func TestEnrollmentPlan_PreflightPassesWhenAllAvailable(t *testing.T) {
	reg := registryWith(t,
		&stubFactor{name: string(FactorMTLS), available: true},
		&stubFactor{name: string(FactorFIDO2), available: true},
		&stubFactor{name: string(FactorPIV), available: true},
	)
	plan, err := NewEnrollmentPlan(EnrollableAdminFactors())
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	if err := plan.Preflight(context.Background(), reg); err != nil {
		t.Fatalf("Preflight = %v, want nil", err)
	}
}

func TestEnrollmentPlan_PreflightRejectsNilRegistry(t *testing.T) {
	plan, err := NewEnrollmentPlan(nil)
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	if err := plan.Preflight(context.Background(), nil); !errors.Is(err, ErrNilFactorRegistry) {
		t.Fatalf("Preflight(nil registry) = %v, want ErrNilFactorRegistry", err)
	}
}

func TestEnrollmentPlan_NilPlanBehavesAsCertificateOnly(t *testing.T) {
	var plan *EnrollmentPlan
	if got := plan.Factors(); len(got) != 1 || got[0] != FactorMTLS {
		t.Fatalf("nil plan Factors() = %v, want [cert]", got)
	}
	if !plan.Includes(FactorMTLS) {
		t.Error("nil plan must include the certificate baseline")
	}
	if plan.Includes(FactorFIDO2) {
		t.Error("nil plan must not include a passkey")
	}
}

func TestEnrollmentPlan_PreflightTreatsFactorWithoutProbeAsAvailable(t *testing.T) {
	// A factor that cannot fail for environmental reasons (mTLS needs only a
	// CA) does not implement FactorAvailability, and must not be penalised
	// for it. This is why the probe is an optional type assertion rather than
	// a method on authfactors.Factor: adding it there would force every
	// existing factor in every consumer to grow a method that always returns
	// true.
	reg := registryWith(t, &plainFactor{name: string(FactorMTLS)})
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	if err := plan.Preflight(context.Background(), reg); err != nil {
		t.Fatalf("Preflight = %v, want nil for a factor with no availability probe", err)
	}
}

func TestEnrollmentPlan_PreflightUsesTheCanonicalRegistry(t *testing.T) {
	// Proof that the ceremony reuses pkg/authfactors rather than a parallel
	// registry: a factor registered through the canonical API is found here.
	reg := authfactors.NewFactorRegistry()
	if err := reg.Register(&stubFactor{name: string(FactorFIDO2), available: true}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	if err := plan.Preflight(context.Background(), reg); err != nil {
		t.Fatalf("Preflight = %v, want nil", err)
	}
}

func TestParseAdminFactors_DefaultsToMTLS(t *testing.T) {
	plan, err := ParseAdminFactors(nil)
	if err != nil {
		t.Fatalf("ParseAdminFactors(nil): %v", err)
	}
	if got := plan.Factors(); len(got) != 1 || got[0] != FactorMTLS {
		t.Fatalf("default = %v, want [mtls]", got)
	}
}

func TestParseAdminFactors_AcceptsRepeatedAndCommaSeparated(t *testing.T) {
	// Both spellings must mean the same thing, in every binary: an operator
	// should not have to remember which daemon wants which form.
	repeated, err := ParseAdminFactors([]string{"mtls", "fido2", "piv"})
	if err != nil {
		t.Fatalf("repeated: %v", err)
	}
	joined, err := ParseAdminFactors([]string{"mtls,fido2", "piv"})
	if err != nil {
		t.Fatalf("comma-separated: %v", err)
	}
	if len(repeated.Factors()) != len(joined.Factors()) {
		t.Fatalf("repeated=%v joined=%v, want identical", repeated.Factors(), joined.Factors())
	}
	for i, f := range repeated.Factors() {
		if joined.Factors()[i] != f {
			t.Fatalf("repeated=%v joined=%v, want identical", repeated.Factors(), joined.Factors())
		}
	}
}

func TestParseAdminFactors_NormalisesCaseAndWhitespace(t *testing.T) {
	plan, err := ParseAdminFactors([]string{"  MTLS ", "", " FiDo2 "})
	if err != nil {
		t.Fatalf("ParseAdminFactors: %v", err)
	}
	got := plan.Factors()
	if len(got) != 2 || got[0] != FactorMTLS || got[1] != FactorFIDO2 {
		t.Fatalf("plan = %v, want [mtls fido2]", got)
	}
}

func TestParseAdminFactors_RejectsUnknownAndNamesWhatIsAccepted(t *testing.T) {
	_, err := ParseAdminFactors([]string{"mtls", "password"})
	var unsupported *UnsupportedFactorError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *UnsupportedFactorError", err)
	}
	// The message must list the alternatives; a bare rejection sends the
	// operator to the source.
	for _, name := range AdminFactorNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention accepted factor %q", err, name)
		}
	}
}

func TestParseAdminFactors_RejectsPKCS11AtInit(t *testing.T) {
	// A valid login factor, but not bootstrappable: it needs a configured
	// backend that does not exist yet at first boot.
	_, err := ParseAdminFactors([]string{"pkcs11"})
	var unsupported *UnsupportedFactorError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want *UnsupportedFactorError", err)
	}
}

func TestAdminFactorNames_IsStable(t *testing.T) {
	got := AdminFactorNames()
	want := []string{"fido2", "mtls", "piv"}
	if len(got) != len(want) {
		t.Fatalf("AdminFactorNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AdminFactorNames() = %v, want %v", got, want)
		}
	}
}

func TestEnrollAdminFactors_EnrollsInPlanOrder(t *testing.T) {
	mtls := &stubFactor{name: string(FactorMTLS), available: true}
	fido := &stubFactor{name: string(FactorFIDO2), available: true}
	reg := registryWith(t, fido, mtls)

	plan, err := NewEnrollmentPlan([]AdminFactor{FactorFIDO2, FactorMTLS})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	results, err := EnrollAdminFactors(context.Background(), plan, reg, "admin", "system", nil, nil)
	if err != nil {
		t.Fatalf("EnrollAdminFactors: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	// mTLS is reordered to the front by the plan and must enroll first: it is
	// the credential that still works if the hardware ceremony is interrupted.
	if results[0].Factor != FactorMTLS {
		t.Errorf("first enrolled = %q, want mtls", results[0].Factor)
	}
	if !mtls.enrolled || !fido.enrolled {
		t.Error("both factors should have enrolled")
	}
}

func TestEnrollAdminFactors_PassesMethodParamsThrough(t *testing.T) {
	// The provider is how hardware stays out of qrdb: go-xkms drives CTAP2 and
	// hands back the finished credential inputs.
	fido := &stubFactor{name: string(FactorFIDO2), available: true}
	reg := registryWith(t, fido)
	plan, err := NewEnrollmentPlan([]AdminFactor{FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	var seen map[string]any
	provider := func(_ context.Context, f AdminFactor) (map[string]any, error) {
		if f != FactorFIDO2 {
			t.Errorf("provider called for %q, want fido2", f)
		}
		return map[string]any{"credential_id": "abc"}, nil
	}
	fido.captureParams = func(p map[string]any) { seen = p }

	if _, err := EnrollAdminFactors(context.Background(), plan, reg, "admin", "system", provider, nil); err != nil {
		t.Fatalf("EnrollAdminFactors: %v", err)
	}
	if seen["credential_id"] != "abc" {
		t.Errorf("MethodParams = %v, want the provider's params", seen)
	}
}

func TestEnrollAdminFactors_ReportsWhatSucceededOnFailure(t *testing.T) {
	// A partial failure must name the factor and report the factors that ARE
	// live, rather than implying the administrator has no credentials.
	mtls := &stubFactor{name: string(FactorMTLS), available: true}
	fido := &stubFactor{name: string(FactorFIDO2), available: true, enrollErr: errors.New("user presence timed out")}
	reg := registryWith(t, mtls, fido)

	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS, FactorFIDO2})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	results, err := EnrollAdminFactors(context.Background(), plan, reg, "admin", "system", nil, nil)
	var enrollErr *FactorEnrollmentError
	if !errors.As(err, &enrollErr) {
		t.Fatalf("err = %v, want *FactorEnrollmentError", err)
	}
	if enrollErr.Factor != FactorFIDO2 {
		t.Errorf("Factor = %q, want fido2", enrollErr.Factor)
	}
	if len(results) != 1 || results[0].Factor != FactorMTLS {
		t.Errorf("results = %v, want the mtls enrollment that succeeded", results)
	}
}

func TestEnrollAdminFactors_RejectsNilRegistry(t *testing.T) {
	plan, err := NewEnrollmentPlan(nil)
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}
	if _, err := EnrollAdminFactors(context.Background(), plan, nil, "admin", "system", nil, nil); !errors.Is(err, ErrNilFactorRegistry) {
		t.Fatalf("err = %v, want ErrNilFactorRegistry", err)
	}
}

// TestEnrollAdminFactors_PersistsEachEnrollment pins the seam that gives an
// enrolled credential a home.
//
// Factor.Enroll is pure — it drives the authenticator and returns public
// credential material, writing nothing. Before this seam existed the results
// were returned, logged, and dropped, so a passkey could enroll "successfully"
// while existing on the operator's token and nowhere on the server: enrolled in
// appearance only, and unable to authenticate.
func TestEnrollAdminFactors_PersistsEachEnrollment(t *testing.T) {
	reg := authfactors.NewFactorRegistry()
	if err := reg.Register(&stubFactor{name: "mtls"}); err != nil {
		t.Fatalf("Register(mtls): %v", err)
	}
	if err := reg.Register(&stubFactor{name: "piv"}); err != nil {
		t.Fatalf("Register(piv): %v", err)
	}

	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS, FactorPIV})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	var persisted []string
	persister := AdminFactorPersisterFunc(
		func(_ context.Context, f AdminFactor, e *authfactors.Enrollment) error {
			if e == nil {
				t.Error("persister received a nil enrollment")
			}
			persisted = append(persisted, f.String())
			return nil
		})

	results, err := EnrollAdminFactors(
		context.Background(), plan, reg, "admin", "system", nil, persister)
	if err != nil {
		t.Fatalf("EnrollAdminFactors: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if len(persisted) != len(results) {
		t.Fatalf("persisted %d factors but enrolled %d; every enrollment must be stored",
			len(persisted), len(results))
	}
	// mTLS enrolls first — it is the credential that survives an interrupted
	// hardware ceremony — so it must also be persisted first.
	if persisted[0] != "mtls" {
		t.Errorf("first persisted factor = %q, want mtls", persisted[0])
	}
}

// TestEnrollAdminFactors_PersistFailureStopsAndReports covers a store that
// refuses.
//
// A factor that enrolled but could not be stored is worse than one that never
// enrolled: the operator's authenticator now holds a credential the server will
// not recognise. Continuing to the next factor would compound that, so
// enrollment stops — and the factors that DID persist are returned alongside
// the error, so the operator is told exactly which credentials are live.
func TestEnrollAdminFactors_PersistFailureStopsAndReports(t *testing.T) {
	reg := authfactors.NewFactorRegistry()
	if err := reg.Register(&stubFactor{name: "mtls"}); err != nil {
		t.Fatalf("Register(mtls): %v", err)
	}
	if err := reg.Register(&stubFactor{name: "piv"}); err != nil {
		t.Fatalf("Register(piv): %v", err)
	}

	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS, FactorPIV})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	storeErr := errors.New("factor store unavailable")
	var attempts int
	persister := AdminFactorPersisterFunc(
		func(_ context.Context, f AdminFactor, _ *authfactors.Enrollment) error {
			attempts++
			if f == FactorPIV {
				return storeErr
			}
			return nil
		})

	results, err := EnrollAdminFactors(
		context.Background(), plan, reg, "admin", "system", nil, persister)

	if !errors.Is(err, storeErr) {
		t.Fatalf("err = %v, want it to wrap the store error", err)
	}
	if attempts != 2 {
		t.Fatalf("persister called %d times, want 2 (stop at the failing factor)", attempts)
	}
	if len(results) != 1 || results[0].Factor != FactorMTLS {
		t.Fatalf("results = %v, want only the mtls factor that persisted successfully", results)
	}
}

// TestEnrollAdminFactors_NilPersisterEnrollsWithoutStoring keeps the older
// probe-only behaviour reachable, so a caller that genuinely only wants to
// exercise hardware is not forced to invent a store.
func TestEnrollAdminFactors_NilPersisterEnrollsWithoutStoring(t *testing.T) {
	reg := authfactors.NewFactorRegistry()
	if err := reg.Register(&stubFactor{name: "mtls"}); err != nil {
		t.Fatalf("Register(mtls): %v", err)
	}

	plan, err := NewEnrollmentPlan([]AdminFactor{FactorMTLS})
	if err != nil {
		t.Fatalf("NewEnrollmentPlan: %v", err)
	}

	results, err := EnrollAdminFactors(
		context.Background(), plan, reg, "admin", "system", nil, nil)
	if err != nil {
		t.Fatalf("EnrollAdminFactors with a nil persister: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}
