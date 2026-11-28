package user

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// racingKVStore runs hook once, right after the first Get of watchKey
// returns, so a commit lands between ActivatePending's read and its write.
type racingKVStore struct {
	*memKVStore
	watchKey string
	fired    atomic.Bool
	hook     func()
}

func (r *racingKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.memKVStore.Get(ctx, key)
	if key == r.watchKey && r.hook != nil && r.fired.CompareAndSwap(false, true) {
		r.hook()
	}
	return v, err
}

func TestActivatePending_ActivatesAPendingAccount(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, err := s.ActivatePending(ctx, u.ID)
	if err != nil {
		t.Fatalf("ActivatePending: %v", err)
	}
	if got.Lifecycle != LifecycleActive {
		t.Fatalf("returned lifecycle = %s, want active", got.Lifecycle)
	}
	stored, _ := s.GetUser(ctx, u.ID)
	if stored.Lifecycle != LifecycleActive {
		t.Fatalf("stored lifecycle = %s, want active", stored.Lifecycle)
	}
}

func TestActivatePending_RefusesAnAccountThatIsNotPending(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	active, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "active", TenantID: "acme", WithoutEmailLoginKey: true})
	disabled, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "off", TenantID: "acme", WithoutEmailLoginKey: true, Lifecycle: LifecycleDisabled})
	deleting, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "gone", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	deleting.Lifecycle = LifecycleDeleting
	raw, err := json.Marshal(deleting)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := s.store.Put(ctx, primaryKey(deleting.ID), raw); err != nil {
		t.Fatalf("put deleting record: %v", err)
	}

	for _, u := range []*User{active, disabled, deleting} {
		before, _ := s.store.Get(ctx, primaryKey(u.ID))
		_, err := s.ActivatePending(ctx, u.ID)
		var np *AccountNotPendingError
		if !errors.As(err, &np) || np.ID != u.ID || np.Lifecycle != u.Lifecycle {
			t.Fatalf("%s: err = %v, want *AccountNotPendingError for %s", u.Username, err, u.Lifecycle)
		}
		after, _ := s.store.Get(ctx, primaryKey(u.ID))
		if string(before) != string(after) {
			t.Fatalf("%s: a refused activation wrote the record", u.Username)
		}
	}
}

func TestActivatePending_RefusesADisableCommittedBetweenReadAndWrite(t *testing.T) {
	mem := newMemKVStore()
	racing := &racingKVStore{memKVStore: mem}
	s := &kvUserStore{store: racing, idgen: newTestIDGen(t)}
	ctx := context.Background()
	u, err := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	racing.watchKey = primaryKey(u.ID)
	racing.hook = func() {
		if derr := s.DisableUser(ctx, u.ID); derr != nil {
			t.Errorf("DisableUser: %v", derr)
		}
	}

	_, err = s.ActivatePending(ctx, u.ID)
	var np *AccountNotPendingError
	if !errors.As(err, &np) || np.Lifecycle != LifecycleDisabled {
		t.Fatalf("err = %v, want *AccountNotPendingError for disabled", err)
	}
	if !racing.fired.Load() {
		t.Fatal("the racing disable never ran")
	}
	stored, _ := s.GetUser(ctx, u.ID)
	if stored.Lifecycle != LifecycleDisabled {
		t.Fatalf("stored lifecycle = %s, want disabled", stored.Lifecycle)
	}
}

func TestActivatePending_ConcurrentCallsHaveOneWinner(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, &CreateUserRequest{Username: "invitee", TenantID: "acme", WithoutEmailLoginKey: true, Pending: true})

	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.ActivatePending(ctx, u.ID)
		}()
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			continue
		}
		var np *AccountNotPendingError
		var lost *ConcurrentUpdateError
		if !errors.As(err, &np) && !errors.As(err, &lost) {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d activations committed, want exactly 1", winners)
	}
	stored, _ := s.GetUser(ctx, u.ID)
	if stored.Lifecycle != LifecycleActive {
		t.Fatalf("stored lifecycle = %s, want active", stored.Lifecycle)
	}
}

func TestActivatePending_RejectsZeroIDAndMissingUser(t *testing.T) {
	s := newActivationStore(t)
	ctx := context.Background()
	var ve *ValidationError
	if _, err := s.ActivatePending(ctx, 0); !errors.As(err, &ve) {
		t.Fatalf("zero id: err = %v, want *ValidationError", err)
	}
	var nf *UserNotFoundError
	if _, err := s.ActivatePending(ctx, 424242); !errors.As(err, &nf) {
		t.Fatalf("missing user: err = %v, want *UserNotFoundError", err)
	}
}
