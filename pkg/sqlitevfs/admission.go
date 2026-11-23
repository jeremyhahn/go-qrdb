package sqlitevfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ErrNilAdmissionChecker is returned by Admit when a new database needs a
// check and the entitlement or quota checker is nil: admission fails closed.
var ErrNilAdmissionChecker = errors.New("sqlitevfs: nil entitlement or quota checker")

// Admit is the creation gate of Q5 A (docs/sqlite-vfs/architecture.md): a
// tenant may create a SQLite database in its page store only with the
// quota.FeatureSQLiteVFS entitlement and while its count of databases is
// under its quota.ResourceSQLiteDatabases limit.
//
// Admit takes the tenant's Backend and checkers as inputs and picks no place
// where a Store is constructed: the caller that will construct one (still
// open, o1) calls Admit before NewStore. It validates db first
// (ValidateDatabaseName). A database whose meta record is live at the newest
// revision already exists, and opening it is not creation: Admit returns nil
// without either check. Otherwise it calls ents.CheckFeature (a
// *quota.EntitlementError is returned unchanged) and then lim.Check for
// quota.ResourceSQLiteDatabases (a *quota.ExceededError likewise). The
// caller registers CountDatabases over the tenant's Backend as that
// resource's counter. The system tenant is implicitly entitled and
// uncapped, as tenant.Service.CheckFeature treats it.
//
// Admit does not create the database: its first committed Txn does. The
// check and that commit are not atomic, so two concurrent creators can both
// pass at the limit minus one; the count cap is a soft bound on growth by
// design, and the tenant's storage quota is the hard bound.
func Admit(ctx context.Context, b Backend, tenantID, db string, ents quota.EntitlementChecker, lim quota.Checker) error {
	metaKey, err := MetaKey(db)
	if err != nil {
		return err
	}
	if b == nil {
		return ErrNilBackend
	}
	rev, err := b.Revision(ctx)
	if err != nil {
		return fmt.Errorf("sqlitevfs: admit %q: %w", db, err)
	}
	_, found, err := b.GetAt(ctx, metaKey, rev)
	if err != nil {
		return fmt.Errorf("sqlitevfs: admit %q: read meta at revision %d: %w", db, rev, err)
	}
	if found || tenantid.IsSystem(tenantID) {
		return nil
	}
	if ents == nil || lim == nil {
		return ErrNilAdmissionChecker
	}
	if err := ents.CheckFeature(ctx, tenantID, quota.FeatureSQLiteVFS); err != nil {
		return err
	}
	return lim.Check(ctx, tenantID, quota.ResourceSQLiteDatabases)
}

// CountDatabases returns the number of databases in b's page store at its
// newest revision: the keys under sqlite/ that are a meta record
// (sqlite/<db>/meta). Page, stage and writer keys never count, including a
// page key whose page-number bytes happen to spell "meta", since a database
// name holds no '/'. It is the quota.ResourceSQLiteDatabases counter for the
// tenant whose scope b reads.
func CountDatabases(ctx context.Context, b Backend) (int64, error) {
	if b == nil {
		return 0, ErrNilBackend
	}
	rev, err := b.Revision(ctx)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: count databases: %w", err)
	}
	keys, err := b.Keys(ctx, []byte(keyRoot), rev)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: count databases: %w", err)
	}
	var n int64
	for _, key := range keys {
		if isMetaKey(key) {
			n++
		}
	}
	return n, nil
}

// isMetaKey reports whether key is sqlite/<db>/meta for a name db with no
// '/'.
func isMetaKey(key []byte) bool {
	if len(key) <= len(keyRoot)+len(metaSuffix) ||
		!bytes.HasPrefix(key, []byte(keyRoot)) || !bytes.HasSuffix(key, []byte(metaSuffix)) {
		return false
	}
	name := key[len(keyRoot) : len(key)-len(metaSuffix)]
	return len(name) > 0 && bytes.IndexByte(name, '/') < 0
}
