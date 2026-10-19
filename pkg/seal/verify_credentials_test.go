package seal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// newVerifyBarrier returns an initialized, unsealed barrier over backend
// whose deferred strategy checks the credential it is handed, so a wrong
// passphrase is actually refused rather than accepted by a bound strategy.
func newVerifyBarrier(t *testing.T, backend StorageBackend, pass string) *StorageBarrier {
	t.Helper()
	b, err := NewStorageBarrier(testLogger(), backend,
		BarrierConfig{RootKeyPath: "verify/root-key"}, NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), MustCredentials(context.Background(), []byte(pass))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return b
}

func TestStorageBarrier_VerifyCredentials(t *testing.T) {
	const pass = "verify-credentials-pass"
	errBackend := errors.New("backend read failed")

	tests := []struct {
		name    string
		creds   string
		prepare func(t *testing.T, b *StorageBarrier, fb *failingBackend)
		want    error
	}{
		{name: "matching credentials", creds: pass},
		{name: "wrong credentials", creds: "not-the-pass", want: ErrCredentialMismatch},
		{
			name:  "sealed",
			creds: pass,
			prepare: func(t *testing.T, b *StorageBarrier, _ *failingBackend) {
				if err := b.Seal(); err != nil {
					t.Fatalf("Seal: %v", err)
				}
			},
			want: ErrSealed,
		},
		{
			name:  "root key absent",
			creds: pass,
			prepare: func(t *testing.T, b *StorageBarrier, fb *failingBackend) {
				if err := fb.inner.Delete(context.Background(), b.config.RootKeyPath); err != nil {
					t.Fatalf("Delete: %v", err)
				}
			},
			want: ErrNotInitialized,
		},
		{
			name:  "backend read error",
			creds: pass,
			prepare: func(_ *testing.T, _ *StorageBarrier, fb *failingBackend) {
				fb.getErr = errBackend
			},
			want: errBackend,
		},
		{
			name:  "corrupt root key blob",
			creds: pass,
			prepare: func(t *testing.T, b *StorageBarrier, fb *failingBackend) {
				if err := fb.inner.Put(context.Background(), b.config.RootKeyPath, []byte("{not json")); err != nil {
					t.Fatalf("Put: %v", err)
				}
			},
			want: ErrCorruptRootKey,
		},
		{
			name:  "unregistered strategy",
			creds: pass,
			prepare: func(t *testing.T, b *StorageBarrier, fb *failingBackend) {
				ctx := context.Background()
				blob, err := fb.inner.Get(ctx, b.config.RootKeyPath)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				var sealed SealedRootKey
				if err := json.Unmarshal(blob, &sealed); err != nil {
					t.Fatalf("Unmarshal: %v", err)
				}
				sealed.Strategy = StrategyID("unregistered-strategy")
				blob, err = json.Marshal(&sealed)
				if err != nil {
					t.Fatalf("Marshal: %v", err)
				}
				if err := fb.inner.Put(ctx, b.config.RootKeyPath, blob); err != nil {
					t.Fatalf("Put: %v", err)
				}
			},
			want: ErrStrategyNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFailingBackend()
			b := newVerifyBarrier(t, fb, pass)
			if tc.prepare != nil {
				tc.prepare(t, b, fb)
			}
			creds := MustCredentials(context.Background(), []byte(tc.creds))
			defer DestroyCredentials(creds)

			err := b.VerifyCredentials(context.Background(), creds)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("VerifyCredentials = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("VerifyCredentials = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestStorageBarrier_CeremonyDistributor(t *testing.T) {
	b := newVerifyBarrier(t, NewMemoryBackend(), "distributor-pass")
	if got := b.CeremonyDistributor(); got != nil {
		t.Fatalf("CeremonyDistributor before Set = %v, want nil", got)
	}
	d := &captureDistributor{}
	b.SetCeremonyDistributor(d)
	if got := b.CeremonyDistributor(); got != d {
		t.Fatalf("CeremonyDistributor = %v, want the installed distributor", got)
	}
}

func TestTenantBarrier_CompareAndDelete(t *testing.T) {
	ctx := context.Background()
	tb := newInitializedTenantBarrier(t, "cad", NewMemoryBackend())
	kv, err := NewTenantBarrierKVStore(tb)
	if err != nil {
		t.Fatalf("NewTenantBarrierKVStore: %v", err)
	}
	store, ok := kv.(guardedDeleteBackend)
	if !ok {
		t.Fatalf("tenant kvstore %T has no guarded delete", kv)
	}

	tests := []struct {
		name string
		del  func(key string, expected []byte) (bool, error)
	}{
		{name: "tenant barrier", del: func(k string, e []byte) (bool, error) { return tb.CompareAndDelete(ctx, k, e) }},
		{name: "kvstore wrapper", del: func(k string, e []byte) (bool, error) { return store.CompareAndDelete(ctx, k, e) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := "cad/" + tc.name
			if err := tb.Put(ctx, key, []byte("v1")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if ok, err := tc.del(key, []byte("other")); err != nil || ok {
				t.Fatalf("CompareAndDelete(mismatch) = (%v, %v), want (false, nil)", ok, err)
			}
			if ok, err := tc.del(key, []byte("v1")); err != nil || !ok {
				t.Fatalf("CompareAndDelete(match) = (%v, %v), want (true, nil)", ok, err)
			}
			if ok, err := tc.del(key, []byte("v1")); err != nil || ok {
				t.Fatalf("CompareAndDelete(absent) = (%v, %v), want (false, nil)", ok, err)
			}
		})
	}

	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := store.CompareAndDelete(ctx, "cad/any", nil); !errors.Is(err, ErrSealed) {
		t.Fatalf("CompareAndDelete(sealed) = %v, want ErrSealed", err)
	}
}

func TestStorageBarrier_ExportRootKeyContext_Sealed(t *testing.T) {
	b := newVerifyBarrier(t, NewMemoryBackend(), "export-pass")
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := b.ExportRootKeyContext(context.Background()); !errors.Is(err, ErrSealed) {
		t.Fatalf("ExportRootKeyContext(sealed) = %v, want ErrSealed", err)
	}
}

func TestStorageBarrier_LoadMigrationJournal_Faults(t *testing.T) {
	errBackend := errors.New("journal read failed")
	var syntaxErr *json.SyntaxError

	tests := []struct {
		name    string
		prepare func(t *testing.T, b *StorageBarrier, fb *failingBackend)
		check   func(t *testing.T, j *MigrationJournal, err error)
	}{
		{
			name: "absent journal",
			check: func(t *testing.T, j *MigrationJournal, err error) {
				if err != nil || j != nil {
					t.Fatalf("LoadMigrationJournal = (%v, %v), want (nil, nil)", j, err)
				}
			},
		},
		{
			name: "empty journal",
			prepare: func(t *testing.T, b *StorageBarrier, fb *failingBackend) {
				if err := fb.inner.Put(context.Background(), b.migrationJournalPath(), []byte{}); err != nil {
					t.Fatalf("Put: %v", err)
				}
			},
			check: func(t *testing.T, j *MigrationJournal, err error) {
				if err != nil || j != nil {
					t.Fatalf("LoadMigrationJournal = (%v, %v), want (nil, nil)", j, err)
				}
			},
		},
		{
			name: "corrupt journal",
			prepare: func(t *testing.T, b *StorageBarrier, fb *failingBackend) {
				if err := fb.inner.Put(context.Background(), b.migrationJournalPath(), []byte("{bad")); err != nil {
					t.Fatalf("Put: %v", err)
				}
			},
			check: func(t *testing.T, j *MigrationJournal, err error) {
				if !errors.As(err, &syntaxErr) || j != nil {
					t.Fatalf("LoadMigrationJournal = (%v, %v), want a *json.SyntaxError", j, err)
				}
			},
		},
		{
			name: "backend read error",
			prepare: func(_ *testing.T, _ *StorageBarrier, fb *failingBackend) {
				fb.getErr = errBackend
			},
			check: func(t *testing.T, j *MigrationJournal, err error) {
				if !errors.Is(err, errBackend) || j != nil {
					t.Fatalf("LoadMigrationJournal = (%v, %v), want %v", j, err, errBackend)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFailingBackend()
			b := newVerifyBarrier(t, fb, "journal-pass")
			if tc.prepare != nil {
				tc.prepare(t, b, fb)
			}
			j, err := b.LoadMigrationJournal(context.Background())
			tc.check(t, j, err)
		})
	}
}
