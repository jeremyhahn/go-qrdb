//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"reflect"
	"syscall"
	"testing"
)

var errBoom = errors.New("boom")

func signals(sigs ...syscall.Signal) []call {
	out := make([]call, len(sigs))
	for i, s := range sigs {
		out[i] = call{Op: "signal", Sig: s}
	}
	return out
}

func assertCalls(t *testing.T, f *fakeTarget, want []call) {
	t.Helper()
	if got := f.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s calls = %+v, want %+v", f.name, got, want)
	}
}

func TestKillInjectsSigkillAndHealRestarts(t *testing.T) {
	ctx := context.Background()
	a, b := newRestarter("n1"), newRestarter("n2")
	k := Kill()
	if k.Name() != "kill" {
		t.Fatalf("name = %q", k.Name())
	}
	if err := k.Inject(ctx, targets(a, b)); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, signals(syscall.SIGKILL))
	assertCalls(t, b.fakeTarget, signals(syscall.SIGKILL))
	if err := k.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, []call{{Op: "restart"}})
	assertCalls(t, b.fakeTarget, []call{{Op: "restart"}})
	if err := k.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, nil)
}

func TestKillRefusesTargetThatCannotRestartBeforeKillingAny(t *testing.T) {
	a, plain := newRestarter("n1"), newFake("n2")
	k := Kill()
	err := k.Inject(context.Background(), []Target{a, plain})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	assertCalls(t, a.fakeTarget, nil)
	assertCalls(t, plain, nil)
	if err := k.Heal(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, nil)
}

func TestKillSealedHealRestartsThenUnseals(t *testing.T) {
	ctx := context.Background()
	a, b := newUnsealer("n1"), newUnsealer("n2")
	k := KillSealed()
	if k.Name() != "kill" {
		t.Fatalf("name = %q", k.Name())
	}
	if err := k.Inject(ctx, targets(a, b)); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, signals(syscall.SIGKILL))
	assertCalls(t, b.fakeTarget, signals(syscall.SIGKILL))
	if err := k.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, []call{{Op: "restart"}, {Op: "unseal"}})
	assertCalls(t, b.fakeTarget, []call{{Op: "restart"}, {Op: "unseal"}})
	if err := k.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, nil)
}

func TestKillSealedRetriesAFailedUnseal(t *testing.T) {
	ctx := context.Background()
	a := newUnsealer("n1")
	k := KillSealed()
	if err := k.Inject(ctx, targets(a)); err != nil {
		t.Fatal(err)
	}
	a.take()
	a.fail("unseal", errBoom)
	if err := k.Heal(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("heal with a failing unseal = %v, want errBoom", err)
	}
	assertCalls(t, a.fakeTarget, []call{{Op: "restart"}, {Op: "unseal"}})
	a.fail("unseal", nil)
	if err := k.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, []call{{Op: "restart"}, {Op: "unseal"}})
}

func TestKillSealedRefusesTargetThatCannotUnsealBeforeKillingAny(t *testing.T) {
	a, restartOnly := newUnsealer("n1"), newRestarter("n2")
	k := KillSealed()
	err := k.Inject(context.Background(), []Target{a, restartOnly})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	assertCalls(t, a.fakeTarget, nil)
	assertCalls(t, restartOnly.fakeTarget, nil)
}

func TestPauseInjectsSigstopAndHealSigcont(t *testing.T) {
	ctx := context.Background()
	a := newFake("n1")
	p := Pause()
	if p.Name() != "pause" {
		t.Fatalf("name = %q", p.Name())
	}
	if err := p.Inject(ctx, targets(a)); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a, signals(syscall.SIGSTOP))
	if err := p.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a, signals(syscall.SIGCONT))
	if err := p.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a, nil)
}

func TestHealAfterFailedInjectReachesEveryTouchedTarget(t *testing.T) {
	ctx := context.Background()
	a, b, c := newFake("n1"), newFake("n2"), newFake("n3")
	b.fail("signal", errBoom)
	p := Pause()
	err := p.Inject(ctx, targets(a, b, c))
	var fe *FaultError
	if !errors.As(err, &fe) || fe.Node != "n2" || fe.Op != "inject" || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want inject failure on n2", err)
	}
	assertCalls(t, c, nil)
	a.take()
	b.take()
	b.fail("signal", nil)
	if err := p.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	// n2's stop may have landed before it failed, so it is healed too; n3
	// was never touched.
	assertCalls(t, a, signals(syscall.SIGCONT))
	assertCalls(t, b, signals(syscall.SIGCONT))
	assertCalls(t, c, nil)
}

func TestHealRetriesOnlyTargetsThatFailedToHeal(t *testing.T) {
	ctx := context.Background()
	a, b := newRestarter("n1"), newRestarter("n2")
	k := Kill()
	if err := k.Inject(ctx, targets(a, b)); err != nil {
		t.Fatal(err)
	}
	a.take()
	b.take()
	b.fail("restart", errBoom)
	err := k.Heal(ctx)
	var fe *FaultError
	if !errors.As(err, &fe) || fe.Node != "n2" || fe.Op != "heal" {
		t.Fatalf("err = %v, want heal failure on n2", err)
	}
	if !errors.Is(k.Inject(ctx, targets(a)), ErrUnhealed) {
		t.Fatal("inject over an unhealed fault was accepted")
	}
	a.take()
	b.take()
	b.fail("restart", nil)
	if err := k.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, a.fakeTarget, nil)
	assertCalls(t, b.fakeTarget, []call{{Op: "restart"}})
}

func TestInjectRejectsBadTargetSets(t *testing.T) {
	ctx := context.Background()
	p := Pause()
	if err := p.Inject(ctx, nil); !errors.Is(err, ErrNoTargets) {
		t.Fatalf("empty: err = %v", err)
	}
	a := newFake("n1")
	var ce *ConfigError
	if err := p.Inject(ctx, targets(a, a)); !errors.As(err, &ce) {
		t.Fatalf("duplicate: err = %v", err)
	}
	assertCalls(t, a, nil)
}

func TestInjectUnsupportedTargetIsNotHealed(t *testing.T) {
	ctx := context.Background()
	a, b := newFake("n1"), newFake("n2")
	b.fail("exec", ErrUnsupported)
	d, err := NewDiskFull(DiskFullConfig{Path: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Inject(ctx, targets(a, b)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	a.take()
	b.take()
	if err := d.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.take(); len(got) != 1 {
		t.Fatalf("n1 heal calls = %+v", got)
	}
	assertCalls(t, b, nil)
}

func TestErrorStrings(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ConfigError{Field: "f", Reason: "r"}, "nemesis: invalid f: r"},
		{&ConfigError{Field: "f", Reason: "r", Err: errBoom}, "nemesis: invalid f: r: boom"},
		{&FaultError{Nemesis: "kill", Op: "heal", Node: "n1", Err: errBoom}, "nemesis: kill heal on n1: boom"},
		{&CommandError{Argv: []string{"echo", "a b"}, Err: errBoom}, "nemesis: command echo 'a b': boom"},
		{&CommandError{Argv: []string{"x"}, Output: []byte("out"), Err: errBoom}, "nemesis: command x: boom: out"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
		if tc.err.(interface{ Unwrap() error }).Unwrap() == nil && tc.want != "nemesis: invalid f: r" {
			t.Errorf("%q does not unwrap", tc.want)
		}
	}
}
