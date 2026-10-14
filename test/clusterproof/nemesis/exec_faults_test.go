//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/clockrate"
)

var proofAddrs = map[string][]string{
	"n1": {"10.0.0.1"},
	"n2": {"10.0.0.2"},
	"n3": {"10.0.0.3"},
	"n4": {"10.0.0.4"},
	"n5": {"10.0.0.5", "fd00::5"},
}

func execs(argvs ...[]string) []call {
	out := make([]call, len(argvs))
	for i, a := range argvs {
		out[i] = call{Op: "exec", Argv: a}
	}
	return out
}

// with returns the template prefix up to (not including) ArgvElement,
// followed by args.
func with(tmpl []string, args ...string) []string {
	i := slices.Index(tmpl, ArgvElement)
	if i < 0 {
		i = len(tmpl)
	}
	return append(slices.Clone(tmpl[:i]), args...)
}

func fakes(names ...string) ([]*fakeTarget, []Target) {
	fs := make([]*fakeTarget, len(names))
	for i, n := range names {
		fs[i] = newFake(n)
	}
	return fs, targets(fs...)
}

func TestPartitionGrudges(t *testing.T) {
	leader := IsolateLeader(func(context.Context) (string, error) { return "n2", nil })
	for _, tc := range []struct {
		grudge Grudge
		nodes  []string
		want   map[string][]string
	}{
		{MajorityMinority, []string{"n1", "n2", "n3"}, map[string][]string{
			"n1": {"10.0.0.2", "10.0.0.3"}, "n2": {"10.0.0.1"}, "n3": {"10.0.0.1"},
		}},
		{MajorityMinority, []string{"n3", "n1", "n2", "n4", "n5"}, map[string][]string{
			"n3": {"10.0.0.2", "10.0.0.4", "10.0.0.5", "fd00::5"}, "n1": {"10.0.0.2", "10.0.0.4", "10.0.0.5", "fd00::5"},
			"n2": {"10.0.0.1", "10.0.0.3"}, "n4": {"10.0.0.1", "10.0.0.3"}, "n5": {"10.0.0.1", "10.0.0.3"},
		}},
		{Bridge, []string{"n1", "n2", "n3", "n4", "n5"}, map[string][]string{
			"n1": {"10.0.0.4", "10.0.0.5", "fd00::5"}, "n2": {"10.0.0.4", "10.0.0.5", "fd00::5"}, "n3": nil,
			"n4": {"10.0.0.1", "10.0.0.2"}, "n5": {"10.0.0.1", "10.0.0.2"},
		}},
		{leader, []string{"n1", "n2", "n3"}, map[string][]string{
			"n1": {"10.0.0.2"}, "n2": {"10.0.0.1", "10.0.0.3"}, "n3": {"10.0.0.2"},
		}},
	} {
		t.Run(tc.grudge.Name, func(t *testing.T) {
			ctx := context.Background()
			p, err := NewPartition(PartitionConfig{Grudge: tc.grudge, Addresses: proofAddrs})
			if err != nil {
				t.Fatal(err)
			}
			if p.Name() != "partition-"+tc.grudge.Name {
				t.Fatalf("name = %q", p.Name())
			}
			fs, ts := fakes(tc.nodes...)
			if err := p.Inject(ctx, ts); err != nil {
				t.Fatal(err)
			}
			for _, f := range fs {
				assertCalls(t, f, execs(with(DefaultPartitionDrop, tc.want[f.name]...)))
			}
			for range 2 {
				if err := p.Heal(ctx); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range fs {
				assertCalls(t, f, execs(DefaultPartitionHeal))
			}
		})
	}
}

func TestPartitionCustomTemplates(t *testing.T) {
	ctx := context.Background()
	p, err := NewPartition(PartitionConfig{
		Grudge:    MajorityMinority,
		Addresses: map[string][]string{"a": {"10.1.0.0/16"}, "b": {"10.2.3.4/16"}, "c": {"10.3.0.1"}},
		Drop:      []string{"block", "{{.Node}}", "{{range .Peers}}{{.}},{{end}}"},
		Heal:      []string{"unblock", "{{.Node}}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fs, ts := fakes("a", "b", "c")
	if err := p.Inject(ctx, ts); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fs[0], execs([]string{"block", "a", "10.2.0.0/16,10.3.0.1,"}))
	assertCalls(t, fs[1], execs([]string{"block", "b", "10.1.0.0/16,"}))
	assertCalls(t, fs[2], execs([]string{"block", "c", "10.1.0.0/16,"}))
	if err := p.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fs[2], execs([]string{"unblock", "c"}))
}

func TestPartitionRejectsBadConfig(t *testing.T) {
	for name, cfg := range map[string]PartitionConfig{
		"no grudge":     {Addresses: proofAddrs},
		"shell text":    {Grudge: Bridge, Addresses: map[string][]string{"n1": {"10.0.0.1; reboot"}}},
		"zoned":         {Grudge: Bridge, Addresses: map[string][]string{"n1": {"fe80::1%eth0"}}},
		"no addresses":  {Grudge: Bridge, Addresses: map[string][]string{"n1": nil}},
		"bad template":  {Grudge: Bridge, Addresses: proofAddrs, Drop: []string{"x", "{{.Nope}}"}},
		"unparseable":   {Grudge: Bridge, Addresses: proofAddrs, Heal: []string{"x", "{{"}},
		"leading splat": {Grudge: Bridge, Addresses: proofAddrs, Drop: []string{ArgvElement}},
	} {
		var ce *ConfigError
		if _, err := NewPartition(cfg); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v, want ConfigError", name, err)
		}
	}
}

func TestPartitionRefusesUnplannableTargetsBeforeTouchingAny(t *testing.T) {
	ctx := context.Background()
	stray := Grudge{Name: "stray", Split: func(context.Context, []string) ([][]string, error) {
		return [][]string{{"n1", "x"}}, nil
	}}
	partial := Grudge{Name: "partial", Split: func(context.Context, []string) ([][]string, error) {
		return [][]string{{"n1"}}, nil
	}}
	noLeader := IsolateLeader(func(context.Context) (string, error) { return "", errBoom })
	outsider := IsolateLeader(func(context.Context) (string, error) { return "n9", nil })
	for _, tc := range []struct {
		grudge Grudge
		nodes  []string
	}{
		{MajorityMinority, []string{"n1", "n2"}},
		{Bridge, []string{"n1", "n2"}},
		{IsolateLeader(nil), []string{"n1"}},
		{noLeader, []string{"n1", "n2"}},
		{outsider, []string{"n1", "n2"}},
		{stray, []string{"n1", "n2"}},
		{partial, []string{"n1", "n2"}},
		{MajorityMinority, []string{"n1", "n2", "unknown"}},
	} {
		p, err := NewPartition(PartitionConfig{Grudge: tc.grudge, Addresses: proofAddrs})
		if err != nil {
			t.Fatal(err)
		}
		fs, ts := fakes(tc.nodes...)
		if err := p.Inject(ctx, ts); err == nil {
			t.Errorf("%s over %v: inject succeeded", tc.grudge.Name, tc.nodes)
		}
		if err := p.Heal(ctx); err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			assertCalls(t, f, nil)
		}
	}
}

func TestDiskFull(t *testing.T) {
	ctx := context.Background()
	d, err := NewDiskFull(DiskFullConfig{Path: "/var/lib/qrdb"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Name() != "disk-full" {
		t.Fatalf("name = %q", d.Name())
	}
	fs, ts := fakes("n1", "n2")
	fs[1].fail("exec", errBoom)
	if err := d.Inject(ctx, ts); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	assertCalls(t, fs[0], execs(with(DefaultDiskFill, "/var/lib/qrdb")))
	assertCalls(t, fs[1], execs(with(DefaultDiskFill, "/var/lib/qrdb")))
	fs[1].fail("exec", nil)
	for range 2 {
		if err := d.Heal(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertCalls(t, fs[0], execs(with(DefaultDiskFree, "/var/lib/qrdb")))
	assertCalls(t, fs[1], execs(with(DefaultDiskFree, "/var/lib/qrdb")))
}

func TestDiskFullCustomTemplatesAndBadConfig(t *testing.T) {
	ctx := context.Background()
	d, err := NewDiskFull(DiskFullConfig{Path: "/data", Fill: []string{"fill", "{{.Node}}", "{{.Path}}"}, Free: []string{"free", ArgvElement}})
	if err != nil {
		t.Fatal(err)
	}
	fs, ts := fakes("n1")
	if err := d.Inject(ctx, ts); err != nil {
		t.Fatal(err)
	}
	if err := d.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fs[0], execs([]string{"fill", "n1", "/data"}, []string{"free", "/data"}))
	for _, cfg := range []DiskFullConfig{{Path: "data"}, {Path: "/data/../etc"}, {Path: ""}, {Path: "/data", Free: []string{"{{.X}}"}}} {
		if _, err := NewDiskFull(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}

func TestSlowDisk(t *testing.T) {
	ctx := context.Background()
	cfg := SlowDiskConfig{Device: "259:0", Cgroup: "/sys/fs/cgroup/system.slice/qrdb.service", BytesPerSecond: 1 << 20}
	d, err := NewSlowDisk(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name() != "slow-disk" {
		t.Fatalf("name = %q", d.Name())
	}
	fs, ts := fakes("n1")
	if err := d.Inject(ctx, ts); err != nil {
		t.Fatal(err)
	}
	inner := []string{"259:0", "1048576", cfg.Cgroup}
	assertCalls(t, fs[0], execs(with(DefaultDiskThrottle, inner...)))
	for range 2 {
		if err := d.Heal(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertCalls(t, fs[0], execs(with(DefaultDiskUnthrottle, inner...)))

	cfg.Throttle = []string{"throttle", "{{.Node}}", "{{.Device}}", "{{.BytesPerSecond}}"}
	d, err = NewSlowDisk(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Inject(ctx, ts); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fs[0], execs([]string{"throttle", "n1", "259:0", "1048576"}))
}

func TestSlowDiskRejectsBadConfig(t *testing.T) {
	good := SlowDiskConfig{Device: "8:0", Cgroup: "/sys/fs/cgroup/qrdb", BytesPerSecond: 1}
	for name, mutate := range map[string]func(*SlowDiskConfig){
		"device dash":  func(c *SlowDiskConfig) { c.Device = "8-0" },
		"device words": func(c *SlowDiskConfig) { c.Device = "a:b" },
		"device minor": func(c *SlowDiskConfig) { c.Device = "8:x" },
		"cgroup":       func(c *SlowDiskConfig) { c.Cgroup = "qrdb" },
		"rate":         func(c *SlowDiskConfig) { c.BytesPerSecond = 0 },
		"template":     func(c *SlowDiskConfig) { c.Unthrottle = []string{"{{.Missing}}"} },
	} {
		cfg := good
		mutate(&cfg)
		var ce *ConfigError
		if _, err := NewSlowDisk(cfg); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// exitStatus is the exit status of a command a target ran, as
// *exec.ExitError reports it.
type exitStatus int

func (e exitStatus) Error() string { return "exit status " + strconv.Itoa(int(e)) }

func (e exitStatus) ExitCode() int { return int(e) }

// clockRateArgv is the default clock-rate command with flags.
func clockRateArgv(flags ...string) []string {
	return with(DefaultClockRateCommand, append([]string{"-not-kernel", "harness"}, flags...)...)
}

func TestClockRateChecksSetsAndHealsToNominal(t *testing.T) {
	ctx := context.Background()
	for _, ppm := range []int{-5000, -20000, 300, -5050, 7, 100_000, -100_000} {
		c, err := NewClockRate(ClockRateConfig{PPM: ppm, Harness: "harness"})
		if err != nil {
			t.Fatal(err)
		}
		if c.Name() != "clock-rate" {
			t.Fatalf("name = %q", c.Name())
		}
		fs, ts := fakes("n1", "n2")
		if err := c.Inject(ctx, ts); err != nil {
			t.Fatal(err)
		}
		// Every target is checked before any clock is changed.
		set := clockRateArgv("-ppm", strconv.Itoa(ppm))
		assertCalls(t, fs[0], execs(clockRateArgv("-check"), set))
		assertCalls(t, fs[1], execs(clockRateArgv("-check"), set))
		for range 2 {
			if err := c.Heal(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// The heal restores the nominal rate rather than undoing the change,
		// and the second heal finds nothing pending.
		assertCalls(t, fs[0], execs(clockRateArgv("-ppm", "0")))
		assertCalls(t, fs[1], execs(clockRateArgv("-ppm", "0")))
	}
}

func TestClockRateRefusalTouchesNoClock(t *testing.T) {
	ctx := context.Background()
	for status, want := range map[int]error{
		clockrate.ExitNoSysTime:     clockrate.ErrNoSysTime,
		clockrate.ExitHarnessKernel: clockrate.ErrHarnessKernel,
		clockrate.ExitUsage:         clockrate.ErrUsage,
	} {
		c, err := NewClockRate(ClockRateConfig{PPM: -5000, Harness: "harness"})
		if err != nil {
			t.Fatal(err)
		}
		fs, ts := fakes("n1", "n2")
		cmdErr := &CommandError{Argv: clockRateArgv("-check"), Err: exitStatus(status)}
		fs[1].fail("exec", cmdErr)
		err = c.Inject(ctx, ts)
		var re *RefusedError
		var fe *FaultError
		if !errors.Is(err, want) || !errors.As(err, &re) || !errors.Is(err, cmdErr) {
			t.Fatalf("exit %d: err = %v, want a refusal for %v", status, err, want)
		}
		if !errors.As(err, &fe) || fe.Op != "check" || fe.Node != "n2" {
			t.Fatalf("exit %d: err = %v, want the check on n2", status, err)
		}
		assertCalls(t, fs[0], execs(clockRateArgv("-check")))
		assertCalls(t, fs[1], execs(clockRateArgv("-check")))
		// Nothing was changed, so nothing is pending.
		if err := c.Heal(ctx); err != nil {
			t.Fatal(err)
		}
		assertCalls(t, fs[0], nil)
		if err := c.Inject(ctx, ts[:1]); err != nil {
			t.Fatalf("exit %d: inject after a refusal: %v", status, err)
		}
	}
}

func TestClockRateFailureIsNotARefusal(t *testing.T) {
	ctx := context.Background()
	for name, cause := range map[string]error{
		"exit 1":       exitStatus(clockrate.ExitFailed),
		"exit 127":     exitStatus(127),
		"no exit code": errors.New("connection refused"),
	} {
		c, err := NewClockRate(ClockRateConfig{PPM: 300, Harness: "harness"})
		if err != nil {
			t.Fatal(err)
		}
		fs, ts := fakes("n1")
		fs[0].fail("exec", cause)
		err = c.Inject(ctx, ts)
		var re *RefusedError
		if !errors.Is(err, cause) || errors.As(err, &re) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestClockRateCustomCommandAndBadConfig(t *testing.T) {
	ctx := context.Background()
	command := []string{"ssh", "{{.Node}}", "--", "/opt/proof/clusterproof", "clock-rate", ArgvElement}
	c, err := NewClockRate(ClockRateConfig{PPM: -5000, Harness: "harness", Command: command})
	if err != nil {
		t.Fatal(err)
	}
	fs, ts := fakes("n1")
	if err := c.Inject(ctx, ts); err != nil {
		t.Fatal(err)
	}
	prefix := []string{"ssh", "n1", "--", "/opt/proof/clusterproof", "clock-rate", "-not-kernel", "harness"}
	assertCalls(t, fs[0], execs(append(slices.Clone(prefix), "-check"), append(slices.Clone(prefix), "-ppm", "-5000")))
	for _, cfg := range []ClockRateConfig{
		{},
		{Harness: "harness"},
		{PPM: clockrate.MaxPPM + 1, Harness: "harness"},
		{PPM: -clockrate.MaxPPM - 1, Harness: "harness"},
		{PPM: 100},
		{PPM: 100, Harness: "harness", Command: []string{"clusterproof", "clock-rate"}},
		{PPM: 100, Harness: "harness", Command: []string{"{{.Nope}}", ArgvElement}},
		{PPM: 100, Harness: "harness", Command: []string{"{{", ArgvElement}},
	} {
		if _, err := NewClockRate(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
	var ce *ConfigError
	if _, err := NewClockRate(ClockRateConfig{PPM: 1}); !errors.As(err, &ce) || ce.Field != "clock-rate harness" {
		t.Fatalf("err = %v, want *ConfigError for the harness", err)
	}
}

func TestExecFaultsRefuseLocalProcess(t *testing.T) {
	l, err := NewLocalProcess(LocalConfig{Node: "n1", Process: noProcess, Restart: noRestart})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDiskFull(DiskFullConfig{Path: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Inject(context.Background(), []Target{l}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
