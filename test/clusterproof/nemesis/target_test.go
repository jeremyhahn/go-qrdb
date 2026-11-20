//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestTemplateRender(t *testing.T) {
	tmpl, err := NewTemplate("t", []string{"ssh", "{{.Vars.host}}", "--", "{{shquote .Argv}}", ArgvElement, "{{shquote .Node}}"})
	if err != nil {
		t.Fatal(err)
	}
	data := remoteData{Node: "it's", Vars: map[string]string{"host": "db1.example.com"}, Argv: []string{"sh", "-c", "echo $HOME"}}
	got, err := tmpl.Render(data, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "db1.example.com", "--", `sh -c 'echo $HOME'`, "a", "b", `'it'\''s'`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

func TestTemplateErrors(t *testing.T) {
	for name, argv := range map[string][]string{
		"empty":   nil,
		"parse":   {"x", "{{.A"},
		"leading": {ArgvElement, "x"},
	} {
		var ce *ConfigError
		if _, err := NewTemplate(name, argv); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	tmpl := MustTemplate("t", "x", "{{.Vars.missing}}", "{{shquote 3}}")
	if _, err := tmpl.Render(remoteData{Vars: map[string]string{}}, nil); err == nil {
		t.Fatal("missing key rendered")
	}
	tmpl = MustTemplate("t", "x", "{{shquote 3}}")
	if _, err := tmpl.Render(remoteData{}, nil); err == nil {
		t.Fatal("shquote of an int rendered")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustTemplate did not panic on a bad template")
		}
	}()
	MustTemplate("t")
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain-word_1.2/x:y=z@h%+,": "plain-word_1.2/x:y=z@h%+,",
		"":                          "''",
		"$(reboot)":                 "'$(reboot)'",
	} {
		if got := quoteWord(in); got != want {
			t.Errorf("quoteWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func sshConfig(r Runner) RemoteConfig {
	return RemoteConfig{
		Node:    "n1",
		Vars:    map[string]string{"host": "db1.example.com", "container": "qrdb-1"},
		Exec:    []string{"ssh", "{{.Vars.host}}", "--", "docker", "run", "--rm", "--privileged", "--net=container:{{.Vars.container}}", "fault", "{{shquote .Argv}}"},
		Signal:  []string{"ssh", "{{.Vars.host}}", "--", "docker", "kill", "--signal={{.Signal}}", "{{.Vars.container}}"},
		Restart: []string{"ssh", "{{.Vars.host}}", "--", "docker", "start", "{{.Vars.container}}"},
		Runner:  r,
	}
}

func TestRemoteTargetRendersEveryPrimitive(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{out: []byte("ok")}
	rt, err := NewRemoteTarget(sshConfig(r))
	if err != nil {
		t.Fatal(err)
	}
	if rt.Name() != "n1" {
		t.Fatalf("name = %q", rt.Name())
	}
	out, err := rt.Exec(ctx, []string{"rm", "-f", "/data/a b"})
	if err != nil || string(out) != "ok" {
		t.Fatalf("Exec = %q, %v", out, err)
	}
	if err := rt.Signal(ctx, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if err := rt.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"ssh", "db1.example.com", "--", "docker", "run", "--rm", "--privileged", "--net=container:qrdb-1", "fault", "rm -f '/data/a b'"},
		{"ssh", "db1.example.com", "--", "docker", "kill", "--signal=STOP", "qrdb-1"},
		{"ssh", "db1.example.com", "--", "docker", "start", "qrdb-1"},
	}
	if !reflect.DeepEqual(r.runs, want) {
		t.Fatalf("runs = %q\nwant %q", r.runs, want)
	}
}

func TestRemoteTargetSpliceAndErrors(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{}
	rt, err := NewRemoteTarget(RemoteConfig{Node: "n1", Exec: []string{"docker", "exec", "qrdb-1", ArgvElement}, Signal: []string{"kill", "-{{.Signal}}", "1"}, Restart: []string{"true"}, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Exec(ctx, []string{"sh", "-c", "x y"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.runs[0], []string{"docker", "exec", "qrdb-1", "sh", "-c", "x y"}) {
		t.Fatalf("run = %q", r.runs[0])
	}
	if _, err := rt.Exec(ctx, nil); err == nil {
		t.Fatal("empty argv ran")
	}
	if err := rt.Signal(ctx, syscall.SIGUSR1); err == nil {
		t.Fatal("unnamed signal sent")
	}
	r.err = errBoom
	if err := rt.Restart(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if len(r.runs) != 2 {
		t.Fatalf("runs = %q", r.runs)
	}
}

func TestRemoteTargetUnsealRendersTheOperatorsCommand(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{}
	cfg := sshConfig(r)
	cfg.Vars["unseal"] = "qrdb-unseal-n1"
	cfg.Unseal = []string{"ssh", "{{.Vars.host}}", "--", "{{.Vars.unseal}}"}
	rt, err := NewRemoteTarget(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Unseal(ctx); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"ssh", "db1.example.com", "--", "qrdb-unseal-n1"}}; !reflect.DeepEqual(r.runs, want) {
		t.Fatalf("runs = %q\nwant %q", r.runs, want)
	}
	r.err = errBoom
	if err := rt.Unseal(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("Unseal with a failing command = %v, want errBoom", err)
	}
}

func TestRemoteTargetWithoutUnsealCommandIsUnsupported(t *testing.T) {
	r := &fakeRunner{}
	rt, err := NewRemoteTarget(sshConfig(r))
	if err != nil {
		t.Fatal(err)
	}
	err = rt.Unseal(context.Background())
	var ce *ConfigError
	if !errors.Is(err, ErrUnsupported) || !errors.As(err, &ce) {
		t.Fatalf("Unseal without a command = %v, want a ConfigError wrapping ErrUnsupported", err)
	}
	if len(r.runs) != 0 {
		t.Fatalf("runs = %q", r.runs)
	}
}

func TestNewRemoteTargetValidates(t *testing.T) {
	for name, mutate := range map[string]func(*RemoteConfig){
		"node":        func(c *RemoteConfig) { c.Node = "" },
		"no exec":     func(c *RemoteConfig) { c.Exec = nil },
		"no signal":   func(c *RemoteConfig) { c.Signal = nil },
		"no restart":  func(c *RemoteConfig) { c.Restart = nil },
		"missing var": func(c *RemoteConfig) { c.Restart = []string{"ssh", "{{.Vars.nohost}}"} },
		"unparseable": func(c *RemoteConfig) { c.Signal = []string{"kill", "{{.Signal"} },
		"bad unseal":  func(c *RemoteConfig) { c.Unseal = []string{"ssh", "{{.Vars.nohost}}"} },
	} {
		cfg := sshConfig(&fakeRunner{})
		mutate(&cfg)
		var ce *ConfigError
		if _, err := NewRemoteTarget(cfg); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	rt, err := NewRemoteTarget(RemoteConfig{Node: "n1", Exec: []string{"true"}, Signal: []string{"true"}, Restart: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.runner.(CommandRunner); !ok || rt.vars == nil {
		t.Fatalf("defaults not applied: %+v", rt)
	}
}

func TestCommandRunner(t *testing.T) {
	ctx := context.Background()
	out, err := CommandRunner{}.Run(ctx, []string{"sh", "-c", "echo hi"})
	if err != nil || string(out) != "hi\n" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	_, err = CommandRunner{}.Run(ctx, []string{"sh", "-c", "echo bad >&2; exit 3"})
	var ce *CommandError
	var ee *exec.ExitError
	if !errors.As(err, &ce) || !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v", err)
	}
	var cfg *ConfigError
	if _, err := (CommandRunner{}).Run(ctx, nil); !errors.As(err, &cfg) {
		t.Fatalf("empty: err = %v", err)
	}
}

func noProcess() *os.Process              { return nil }
func noRestart(ctx context.Context) error { return ctx.Err() }

func TestNewLocalProcessValidates(t *testing.T) {
	for _, cfg := range []LocalConfig{
		{Process: noProcess, Restart: noRestart},
		{Node: "n1", Restart: noRestart},
		{Node: "n1", Process: noProcess},
	} {
		var ce *ConfigError
		if _, err := NewLocalProcess(cfg); !errors.As(err, &ce) {
			t.Errorf("%+v: err = %v", cfg, err)
		}
	}
}

func TestLocalProcessRefusesExecAndAbsentProcess(t *testing.T) {
	l, err := NewLocalProcess(LocalConfig{Node: "n1", Process: noProcess, Restart: noRestart})
	if err != nil {
		t.Fatal(err)
	}
	if l.Name() != "n1" {
		t.Fatalf("name = %q", l.Name())
	}
	if _, err := l.Exec(context.Background(), []string{"true"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Exec err = %v", err)
	}
	if err := l.Signal(context.Background(), syscall.SIGCONT); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Signal err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Signal(ctx, syscall.SIGCONT); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Signal err = %v", err)
	}
}

// child is a process this test starts, so signalling it touches nothing the
// test does not own.
type child struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan error
}

func (c *child) start(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return nil
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd, c.done = cmd, make(chan error, 1)
	go func(done chan error) { done <- cmd.Wait() }(c.done)
	return nil
}

func (c *child) process() *os.Process {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return nil
	}
	return c.cmd.Process
}

// reap waits for the current process to exit and forgets it.
func (c *child) reap(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit")
	}
	c.mu.Lock()
	c.cmd = nil
	c.mu.Unlock()
}

func procState(t *testing.T, p *os.Process) byte {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(p.Pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	// The state follows the parenthesised command name.
	return b[strings.LastIndexByte(string(b), ')')+2]
}

func waitState(t *testing.T, p *os.Process, want byte) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for procState(t, p) != want {
		if time.Now().After(deadline) {
			t.Fatalf("process state = %c, want %c", procState(t, p), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLocalProcessPauseAndKillAChildTheTestStarted(t *testing.T) {
	ctx := context.Background()
	c := &child{}
	if err := c.start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p := c.process(); p != nil {
			_ = p.Kill()
			c.reap(t)
		}
	})
	l, err := NewLocalProcess(LocalConfig{Node: "n1", Process: c.process, Restart: c.start})
	if err != nil {
		t.Fatal(err)
	}

	pause := Pause()
	if err := pause.Inject(ctx, []Target{l}); err != nil {
		t.Fatal(err)
	}
	waitState(t, c.process(), 'T')
	if err := pause.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	waitState(t, c.process(), 'S')

	first := c.process().Pid
	kill := Kill()
	if err := kill.Inject(ctx, []Target{l}); err != nil {
		t.Fatal(err)
	}
	c.reap(t)
	if err := kill.Heal(ctx); err != nil {
		t.Fatal(err)
	}
	if p := c.process(); p == nil || p.Pid == first {
		t.Fatalf("heal did not restart the child: %v", p)
	}
}
