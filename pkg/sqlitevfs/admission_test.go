package sqlitevfs

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// admitEnts is an in-package quota.EntitlementChecker: features lists what
// the tenant is granted; calls counts CheckFeature calls.
type admitEnts struct {
	features map[string]bool
	calls    int
}

func (e *admitEnts) CheckIsolation(context.Context, string, string) error { return nil }

func (e *admitEnts) CheckFeature(_ context.Context, tenantID, feature string) error {
	e.calls++
	if e.features[feature] {
		return nil
	}
	return &quota.EntitlementError{TenantID: tenantID, Feature: feature}
}

func (e *admitEnts) CheckBarrierMode(context.Context, string, string) error { return nil }

func (e *admitEnts) CheckVolumePool(context.Context, string, string) error { return nil }

// admitLimit is an in-package quota.Checker for ResourceSQLiteDatabases
// that counts with CountDatabases over b against limit (0 is unlimited).
type admitLimit struct {
	b     Backend
	limit int64
	calls int
}

func (l *admitLimit) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	return l.CheckN(ctx, tenantID, resource, 1)
}

func (l *admitLimit) CheckN(ctx context.Context, tenantID string, resource quota.Resource, n int64) error {
	l.calls++
	if resource != quota.ResourceSQLiteDatabases {
		return errors.New("unexpected resource " + resource.String())
	}
	current, err := CountDatabases(ctx, l.b)
	if err != nil {
		return err
	}
	if l.limit > 0 && current+n > l.limit {
		return &quota.ExceededError{TenantID: tenantID, Resource: resource, Current: current, Limit: l.limit}
	}
	return nil
}

func (l *admitLimit) Usage(ctx context.Context, _ string, _ quota.Resource) (int64, error) {
	return CountDatabases(ctx, l.b)
}

// createDatabase writes one page through a Store, which creates db.
func createDatabase(t *testing.T, b Backend, db string) {
	t.Helper()
	ctx := context.Background()
	s, err := NewStore(b, db)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", db, err)
	}
	txn, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin(%q): %v", db, err)
	}
	if _, err := txn.WriteAt(make([]byte, 512), 0); err != nil {
		t.Fatalf("WriteAt(%q): %v", db, err)
	}
	if _, err := txn.Commit(ctx); err != nil {
		t.Fatalf("Commit(%q): %v", db, err)
	}
}

func mustCount(t *testing.T, b Backend) int64 {
	t.Helper()
	n, err := CountDatabases(context.Background(), b)
	if err != nil {
		t.Fatalf("CountDatabases: %v", err)
	}
	return n
}

func TestAdmitEntitlementOffRefuses(t *testing.T) {
	b := newTestBackend(t)
	ents := &admitEnts{}
	lim := &admitLimit{b: b}
	err := Admit(context.Background(), b, "acme", "app", ents, lim)
	var denied *quota.EntitlementError
	if !errors.As(err, &denied) || denied.Feature != quota.FeatureSQLiteVFS {
		t.Fatalf("Admit without entitlement = %v, want *quota.EntitlementError for %q", err, quota.FeatureSQLiteVFS)
	}
	if lim.calls != 0 {
		t.Errorf("quota checked %d times after an entitlement refusal, want 0", lim.calls)
	}
	if n := mustCount(t, b); n != 0 {
		t.Errorf("CountDatabases after refusal = %d, want 0", n)
	}
	if rev, _ := b.Revision(context.Background()); rev != 0 {
		t.Errorf("revision after refusal = %d, want 0 (nothing written)", rev)
	}
}

func TestAdmitUnderCapAndAtCap(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	ents := &admitEnts{features: map[string]bool{quota.FeatureSQLiteVFS: true}}
	lim := &admitLimit{b: b, limit: 1}
	if err := Admit(ctx, b, "acme", "one", ents, lim); err != nil {
		t.Fatalf("Admit under cap: %v", err)
	}
	if n := mustCount(t, b); n != 0 {
		t.Errorf("Admit created a database: count %d, want 0", n)
	}
	createDatabase(t, b, "one")
	err := Admit(ctx, b, "acme", "two", ents, lim)
	var exceeded *quota.ExceededError
	if !errors.As(err, &exceeded) || exceeded.Resource != quota.ResourceSQLiteDatabases {
		t.Fatalf("Admit at cap = %v, want *quota.ExceededError for ResourceSQLiteDatabases", err)
	}
	if err := Admit(ctx, b, "acme", "one", ents, lim); err != nil {
		t.Errorf("Admit of the existing database at cap: %v", err)
	}
}

func TestAdmitExistingDatabaseNeedsNoCheck(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	createDatabase(t, b, "app")
	ents := &admitEnts{}
	lim := &admitLimit{b: b, limit: 1}
	if err := Admit(ctx, b, "acme", "app", ents, lim); err != nil {
		t.Fatalf("Admit of an existing database, entitlement off and cap full: %v", err)
	}
	if ents.calls != 0 || lim.calls != 0 {
		t.Errorf("checks on an existing database: entitlement %d, quota %d, want 0 and 0", ents.calls, lim.calls)
	}
}

func TestAdmitBadNameRefusedFirst(t *testing.T) {
	b := newTestBackend(t)
	ents := &admitEnts{}
	lim := &admitLimit{b: b}
	for _, name := range []string{"", "a/b", "..", "x\x00"} {
		err := Admit(context.Background(), b, "acme", name, ents, lim)
		if !errors.Is(err, ErrInvalidDatabaseName) {
			t.Errorf("Admit(%q) = %v, want ErrInvalidDatabaseName", name, err)
		}
	}
	if ents.calls != 0 || lim.calls != 0 {
		t.Errorf("checks before name validation: entitlement %d, quota %d, want 0 and 0", ents.calls, lim.calls)
	}
}

func TestAdmitSystemTenantSkipsChecks(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	createDatabase(t, b, "full")
	ents := &admitEnts{}
	lim := &admitLimit{b: b, limit: 1}
	for _, id := range []string{tenantid.System, ""} {
		if err := Admit(ctx, b, id, "new", ents, lim); err != nil {
			t.Errorf("Admit for system tenant %q: %v", id, err)
		}
	}
	if ents.calls != 0 || lim.calls != 0 {
		t.Errorf("system tenant checks: entitlement %d, quota %d, want 0 and 0", ents.calls, lim.calls)
	}
}

func TestAdmitNilCheckersFailClosed(t *testing.T) {
	b := newTestBackend(t)
	if err := Admit(context.Background(), b, "acme", "app", nil, nil); !errors.Is(err, ErrNilAdmissionChecker) {
		t.Errorf("Admit with nil checkers = %v, want ErrNilAdmissionChecker", err)
	}
	if err := Admit(context.Background(), nil, "acme", "app", &admitEnts{}, &admitLimit{}); !errors.Is(err, ErrNilBackend) {
		t.Errorf("Admit with nil backend = %v, want ErrNilBackend", err)
	}
}

func TestCountDatabasesCountsOnlyMetaKeys(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(t)
	if n := mustCount(t, b); n != 0 {
		t.Fatalf("CountDatabases on an empty store = %d, want 0", n)
	}
	createDatabase(t, b, "app")
	createDatabase(t, b, "app2")
	// A page whose page-number bytes spell "meta", a staged page and a
	// writer-shaped key never count.
	pgno := binary.BigEndian.Uint32([]byte("meta"))
	pageKey, err := PageKey("app", pgno)
	if err != nil {
		t.Fatalf("PageKey: %v", err)
	}
	stageKey, err := StageKey("app", "0123456789abcdef0123456789abcdef", 1)
	if err != nil {
		t.Fatalf("StageKey: %v", err)
	}
	writerKey, err := WriterLockKey("app")
	if err != nil {
		t.Fatalf("WriterLockKey: %v", err)
	}
	puts := map[string][]byte{
		string(pageKey):         make([]byte, 512),
		string(stageKey):        make([]byte, 512),
		writerKey:               []byte("x"),
		"sqlite/meta":           []byte("x"),
		"other/app/meta":        []byte("x"),
		"sqlite/app/stage/meta": []byte("x"),
	}
	if _, err := b.Commit(ctx, nil, puts, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if n := mustCount(t, b); n != 2 {
		t.Errorf("CountDatabases = %d, want 2", n)
	}
	if _, err := CountDatabases(ctx, nil); !errors.Is(err, ErrNilBackend) {
		t.Errorf("CountDatabases(nil) = %v, want ErrNilBackend", err)
	}
}
