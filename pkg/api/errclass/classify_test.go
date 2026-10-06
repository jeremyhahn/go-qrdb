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

package errclass

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/certenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// wrapped hides a sample behind one layer of wrapping with an Error method
// of its own. Table rows match through errors.As / errors.Is, so a condition
// that is only recognised unwrapped would be inert in production, where every
// handler wraps; and a zero-valued sample's own Error method may dereference
// a nil cause.
type wrapped struct{ inner error }

func (w wrapped) Error() string { return "wrapped sample" }
func (w wrapped) Unwrap() error { return w.inner }

// minimumRules is a discovery floor: every assertion below is inside a loop
// over the table, and an empty loop reports success.
const minimumRules = 200

func TestRules_DiscoveryFloor(t *testing.T) {
	if RuleCount() < minimumRules {
		t.Fatalf("the table has %d rows, under the floor of %d: a loop over it proves nothing", RuleCount(), minimumRules)
	}
}

// TestRules_EverySampleIsClaimedByItsOwnRow: a row whose own sample it does
// not recognise is a row nothing exercises.
func TestRules_EverySampleIsClaimedByItsOwnRow(t *testing.T) {
	for _, r := range rules {
		if r.name == classedRule.name {
			continue // Classed types live in the transport packages.
		}
		if len(r.samples) == 0 {
			t.Errorf("row %s has no sample", r.name)
		}
		for _, s := range r.samples {
			if _, ok := r.classify(wrapped{s}); !ok {
				t.Errorf("row %s does not recognise its own sample %T", r.name, s)
			}
		}
	}
}

// TestRules_AreNotShadowed feeds every row's samples back through Classify,
// wrapped. A row answered by an EARLIER row with a different class can never
// fire, and its position — not a decision — would be what classifies that
// condition. This is the ordering guard the per-family ladders never had.
func TestRules_AreNotShadowed(t *testing.T) {
	for _, c := range Conditions() {
		if got := Classify(wrapped{c.Sample}); got != c.Want {
			t.Errorf("row %s (%T) is shadowed: Classify says %s, the row says %s", c.Row, c.Sample, got, c.Want)
		}
	}
}

// TestRules_NamesAreUnique: two rows for one type or sentinel means the second
// is dead, and whichever an editor changes may be the wrong one.
func TestRules_NamesAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, r := range rules {
		if seen[r.name] {
			t.Errorf("row %s appears twice", r.name)
		}
		seen[r.name] = true
	}
}

func TestClassify_NilIsUnclassified(t *testing.T) {
	if got := Classify(nil); got != Unclassified {
		t.Errorf("Classify(nil) = %s, want unclassified", got)
	}
}

func TestClassify_UnknownIsUnclassified(t *testing.T) {
	if got := Classify(errors.New("nobody typed this")); got != Unclassified {
		t.Errorf("got %s, want unclassified", got)
	}
}

// TestClassify_DominantRulesWinThroughWrappers pins the orderings the per-family
// ladders each spelled out by hand, and the wrapper-over-cause cases the
// ordered table exists to get right.
func TestClassify_DominantRulesWinThroughWrappers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"closed store inside a propose failure", &service.ProposeError{Err: storage.ErrStorageClosed}, Unavailable},
		{"closed store inside a read failure", &service.ReadError{Err: storage.ErrStorageClosed}, Unavailable},
		{"failed rollback of a taken username", &user.IndexCompensationError{Op: "create", Cause: &user.UsernameExistsError{Username: "ada"}}, DataLoss},
		{"a generic wrapper reports what it wraps", &service.BatchError{Err: &service.KeyNotFoundError{Key: "k"}}, NotFound},
		{"a generic wrapper alone is a fault", &service.ProposeError{Err: errors.New("raft")}, Internal},
		// Wrapper over cause: the rows below each pin that the OUTER condition
		// is the one reported, because it is the more serious or the more
		// specific fact, even though errors.As would find the cause too.
		{"failed rollback over a lost concurrent update", &user.IndexCompensationError{Op: "update", Cause: &user.ConcurrentUpdateError{}}, DataLoss},
		{"unrecorded audit decision over a missing key", &audit.GateError{Component: "authorize", Err: &service.KeyNotFoundError{Key: "k"}}, Unavailable},
		{"sealed tenant inside a user store error", &user.StoreError{Op: "get", Err: tenant.ErrTenantSealed}, Unavailable},
		{"cross-tenant refusal inside a user store error", &user.StoreError{Op: "get", Err: &tenant.CrossTenantDeniedError{}}, PermissionDenied},
		{"closed store inside a time-series failure", &service.TimeSeriesError{Op: "write", Err: storage.ErrStorageClosed}, Unavailable},
		{"registration refused over a missing provider", &service.EmbeddingProviderRegistrationError{Provider: "p", Cause: &service.EmbeddingProviderNotFoundError{Provider: "p"}}, AlreadyExists},
		{"unresolvable reclaim store over a missing tenant", &service.ReclaimStoreUnavailableError{TenantID: "t", Err: &service.TenantNotFoundError{TenantID: "t"}}, Unavailable},
		{"a propose that failed because the caller gave up", &service.ProposeError{Err: context.DeadlineExceeded}, DeadlineExceeded},
		{"a read canceled by the caller", &service.ReadError{Err: context.Canceled}, Canceled},
		{"a typed condition carrying a context cause stays itself", &service.CapabilityUnsupportedError{Message: "x"}, Unimplemented},
	} {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestClassify_ServiceConditionsTheTableOnceMissed pins service conditions
// that reached every transport as an internal fault because no row named
// them: a duplicate registration, an unwired manager, a retryable timeout and
// a refused migration are each something the caller can act on.
func TestClassify_ServiceConditionsTheTableOnceMissed(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"duplicate embedding provider", &service.EmbeddingProviderAlreadyRegisteredError{Name: "p"}, AlreadyExists},
		{"database barrier manager not wired", &service.BarrierManagerNotConfiguredError{}, Unimplemented},
		{"reclaim store unresolvable", &service.ReclaimStoreUnavailableError{TenantID: "t", Err: errors.New("resolver")}, Unavailable},
		{"leader transfer did not complete", &service.LeaderTransferTimeoutError{ShardID: 1}, Unavailable},
		{"shard snapshot export timed out", &service.ShardSnapshotTimeoutError{ShardID: 1}, Unavailable},
		{"factor migration left records behind", &service.FactorMigrationRefusedError{}, FailedPrecondition},
		{"database store keeps no compaction policy", &service.CompactionPolicyUnavailableError{Database: "d"}, Unprocessable},
		{"node keyspace compaction not composed", &service.NodeCompactionUnavailableError{}, Unprocessable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(wrapped{tc.err}); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestClassify_TenantLocationRefusals pins the classes of the refusals that
// keep a tenant's location a pool and slot resolved on each node, bare and
// wrapped by a service layer.
func TestClassify_TenantLocationRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"tenant-root record names a path", &seal.AbsoluteDataRootRecordError{TenantID: "t", DataRoot: "/d"}, FailedPrecondition},
		{"recorded slot does not resolve here", &seal.TenantRootUnresolvedError{TenantID: "t", Pool: "p", Err: errors.New("x")}, FailedPrecondition},
		{"removal outside this node's pool", &seal.DataRootOutsideLocalRootsError{DataRoot: "/d"}, Internal},
		{"node is not a replica of the database", &placement.NotAReplicaError{TenantID: "t", Database: "d", NodeID: "n"}, Unavailable},
		{"placement replica names a path", &placement.ReplicaDataRootError{TenantID: "t", Database: "d", NodeID: "n", DataRoot: "/d"}, FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, e := range map[string]error{"bare": tc.err, "wrapped": wrapped{tc.err}} {
				if got := Classify(e); got != tc.want {
					t.Errorf("%s: got %s, want %s", name, got, tc.want)
				}
			}
		})
	}
}

// A dead RoleBinding is a bad field in the write, whether it arrives bare or
// wrapped by a service layer.
func TestClassify_RoleBindingTenantIsInvalidArgument(t *testing.T) {
	err := &user.RoleBindingTenantError{UserID: 1, UserTenantID: "acme", BindingTenantID: "globex"}
	for name, e := range map[string]error{"bare": err, "wrapped": wrapped{err}} {
		if got := Classify(e); got != InvalidArgument {
			t.Errorf("%s: got %s, want %s", name, got, InvalidArgument)
		}
	}
}

// TestClassify_ResolveErrorDefinitiveCausesAreNotUnavailable is the re-review
// companion to the parity sample authRule carries for a TRANSIENT
// *rbac.ResolveError (authSamples, context.DeadlineExceeded): a DEFINITIVE
// cause must not become a retryable 503 just because it happens to be
// wrapped the same way.
//
// This cannot live in authSamples: authRule's own classifier
// (pkg/auth/middleware.ClassifyAuthError) reports these as Unknown, so
// Conditions() would record "Want: Unclassified" for them, and the generic
// chain actually classifies them further down -- identityRules' own
// RoleNotFoundError/ValidationError rows -- giving a REAL answer that
// disagrees with another row sharing the same (wrong) "Want" bucket. Tried
// first; TestEveryTransportAgreesOnEveryCondition caught it immediately. The
// direct Classify() call below is the correct proof: it exercises the exact
// same full chain every transport's ServiceErrorStatus/Code calls, without
// forcing these two samples through authRule's narrower "is this an auth
// class" lens.
func TestClassify_ResolveErrorDefinitiveCausesAreNotUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"a deleted or never-existent role", &rbac.ResolveError{Err: &role.RoleNotFoundError{ID: "deleted-role"}}, NotFound},
		{"a malformed request", &rbac.ResolveError{Err: &role.ValidationError{Field: "id", Reason: "must not be empty"}}, InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// tc.want is never Unavailable for these cases, so pinning the
			// exact class below already proves the cause did not become a
			// retryable 503 -- a second call asserting only "!= Unavailable"
			// checked nothing the line above had not already checked (BL2, N6).
			if got := Classify(wrapped{tc.err}); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

type selfClassedError struct{}

func (selfClassedError) Error() string     { return "self" }
func (selfClassedError) ErrorClass() Class { return Gone }

func TestClassify_HonoursClassed(t *testing.T) {
	if got := Classify(wrapped{selfClassedError{}}); got != Gone {
		t.Errorf("got %s, want gone", got)
	}
}

// TestClassify_PendingAdminCertStoreError covers go-qrdb#560 review item 8:
// firstboot.PendingAdminCertStoreError wraps a genuine backing-store
// failure (read, write or consume-in-place of the pending-admin-cert
// record) -- a server-side fault, exactly like user.StoreError,
// role.StoreError, cert.StoreError and tenant.StoreError, all Internal --
// and had no table row at all, so every transport fell through to
// Unclassified for it.
func TestClassify_PendingAdminCertStoreError(t *testing.T) {
	err := &firstboot.PendingAdminCertStoreError{Op: "write", Cause: errors.New("sample")}
	if got := Classify(err); got != Internal {
		t.Errorf("Classify(%T) = %s, want %s", err, got, Internal)
	}
}

// TestClassify_OnboardingRows pins the explicit class every onboardingRules
// row answers with -- certificate enrolment, trust-anchor management and
// tenant onboarding -- so a row that silently lost its class (or was
// reordered behind a more general one) fails here, not only in the generic
// loop tests that merely confirm each row recognises its own sample.
func TestClassify_OnboardingRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"invalid CSR", &cert.InvalidCSRError{Reason: "bad key"}, InvalidArgument},
		{"invalid BYOC certificate", &certenroll.InvalidCertificateError{Err: errors.New("decode")}, InvalidArgument},
		{"BYOC identity mismatch", &certenroll.IdentityMismatchError{TenantID: "acme", Username: "ada"}, InvalidArgument},
		{"empty trust-anchor bundle", &trustanchor.EmptyBundleError{}, InvalidArgument},
		{"invalid trust anchor", &trustanchor.InvalidAnchorError{Index: 0, Reason: "expired"}, InvalidArgument},
		{"invalid trust-anchor tenant", &trustanchor.InvalidTenantError{TenantID: "acme"}, InvalidArgument},
		{"BYOC does not chain to a trust anchor", &trustanchor.CertificateNotTrustedError{TenantID: "acme"}, InvalidArgument},
		{"trust anchor not found", &trustanchor.AnchorNotFoundError{TenantID: "acme", Fingerprint: "fp"}, NotFound},
		{"too many trust anchors", &trustanchor.TooManyAnchorsError{TenantID: "acme", Max: 8}, ResourceExhausted},
		{"no trust anchors registered yet", &trustanchor.NoAnchorsError{TenantID: "acme"}, FailedPrecondition},
		{"trust anchors externally managed", &trustanchor.ExternallyManagedError{}, FailedPrecondition},
		{"invalid pending-admin tenant", &tenantadmin.InvalidTenantError{TenantID: "acme"}, InvalidArgument},
		{"cert enrollment field error", &service.CertEnrollmentFieldError{Field: "csr_pem"}, InvalidArgument},
		{"cert enrollment not configured", &service.CertEnrollmentNotConfiguredError{Capability: "certificate issuance"}, Unimplemented},
		{"tenant onboarding field error", &service.TenantOnboardingFieldError{Field: "admin_username"}, InvalidArgument},
		{"tenant onboarding not configured", &service.TenantOnboardingNotConfiguredError{}, Unimplemented},
		{"missing admin credential", &bootstrap.MissingAdminCredentialError{}, InvalidArgument},
		{"conflicting admin credential", &bootstrap.ConflictingAdminCredentialError{}, InvalidArgument},
		{"invalid trust anchors at provisioning", &bootstrap.InvalidTrustAnchorsError{Cause: errors.New("bad PEM")}, InvalidArgument},
		{"missing provisioning seam", &bootstrap.MissingProvisioningSeamError{Seam: "cert signer"}, Unimplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(wrapped{tc.err}); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestClassify_FirstInitSamples pins the trust-on-first-init refusals as
// PermissionDenied: the caller DID present a verified certificate (the
// window is only reached after that), and that identity may not initialize
// THIS tenant's barrier -- the same reading authClassByFailure gives every
// AuthFailureUnauthorized.
func TestClassify_FirstInitSamples(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"wrong certificate for the onboarded admin", &authmw.FirstInitCertificateMismatchError{TenantID: "acme", Username: "ada", PresentedThumbprint: "ff"}},
		{"no first admin onboarded under mtls_binding_policy required", &authmw.FirstInitNotOnboardedError{TenantID: "acme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(wrapped{tc.err}); got != PermissionDenied {
				t.Errorf("got %s, want %s", got, PermissionDenied)
			}
		})
	}
}

// TestAuthClassByFailure_CoversEveryKnownClass: an auth class with no row
// would fall through to the domain rows and be classified by coincidence.
func TestAuthClassByFailure_CoversEveryKnownClass(t *testing.T) {
	for _, c := range []authmw.AuthFailureClass{
		authmw.AuthFailureUnauthenticated,
		authmw.AuthFailureUnauthorized,
		authmw.AuthFailureUnavailable,
		authmw.AuthFailureRateLimited,
		authmw.AuthFailureAuditUnavailable,
	} {
		if _, ok := authClassByFailure[c]; !ok {
			t.Errorf("auth class %s has no row", c)
		}
	}
	if _, ok := authClassByFailure[authmw.AuthFailureUnknown]; ok {
		t.Error("AuthFailureUnknown must fall through to the rest of the table")
	}
}

func TestAll_IsTheWholeEnumerationInOrder(t *testing.T) {
	all := All()
	if len(all) != int(numClasses) {
		t.Fatalf("All has %d classes, want %d", len(all), numClasses)
	}
	for i, c := range all {
		if int(c) != i {
			t.Errorf("All()[%d] = %d", i, c)
		}
	}
}

func TestClass_String(t *testing.T) {
	for _, c := range All() {
		if name, ok := classNames[c]; !ok || c.String() != name {
			t.Errorf("class %d has no name", c)
		}
	}
	if got := numClasses.String(); got != "class(20)" {
		t.Errorf("out-of-range class renders %q", got)
	}
}

// BenchmarkClassify measures the error path's cost: a condition near the top
// of the table, one near the bottom, and the worst case — an error no row
// recognises, which walks every row.
func BenchmarkClassify(b *testing.B) {
	for _, bc := range []struct {
		name string
		err  error
	}{
		{"early row", &service.KeyNotFoundError{Key: "k"}},
		{"late row", &service.ProposeError{Err: errors.New("raft")}},
		{"unclassified", errors.New("nobody typed this")},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = Classify(bc.err)
			}
		})
	}
}

// TestClassify_ClusterSeal pins how every transport answers a cluster seal
// condition, including when the tenant or database service wraps it -- a
// tenant's BarrierError must not turn a retryable unconfirmed record into an
// internal fault, or a lost race into one.
func TestClassify_ClusterSeal(t *testing.T) {
	notConfirmed := &clusterseal.RecordNotConfirmedError{Barrier: clusterseal.TenantBarrier("t"), Op: "seal", Err: errors.New("no quorum")}
	conflict := &clusterseal.SealEpochConflictError{Barrier: clusterseal.NodeBarrier()}
	for _, tc := range []struct {
		name string
		err  error
		want Class
	}{
		{"seal record not confirmed", notConfirmed, Unavailable},
		{"seal record not confirmed inside a tenant barrier error", &tenant.BarrierError{TenantID: "t", Op: "seal", Err: notConfirmed}, Unavailable},
		{"lost seal race", conflict, Aborted},
		{"lost seal race inside a tenant barrier error", &tenant.BarrierError{TenantID: "t", Op: "clear-cluster-seal", Err: conflict}, Aborted},
		{"cluster sealing not active", clusterseal.ErrInactive, FailedPrecondition},
		{"stream ended by a seal", &clusterseal.SealedError{Barrier: clusterseal.NodeBarrier()}, Unavailable},
		{"node cut off from the seal records", &clusterseal.SealStateUnconfirmedError{}, Unavailable},
		{"corrupt seal record", &clusterseal.CorruptRecordError{Barrier: clusterseal.NodeBarrier()}, Internal},
		{"malformed barrier name", &clusterseal.InvalidBarrierError{Name: "x"}, InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(wrapped{tc.err}); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}
