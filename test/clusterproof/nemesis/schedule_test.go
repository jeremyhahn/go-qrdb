//go:build clusterproof

package nemesis

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"
)

func randomConfig(seed int64) RandomConfig {
	return RandomConfig{
		Seed:  seed,
		Nodes: []string{"n1", "n2", "n3", "n4", "n5"},
		Choices: []Choice{
			{Nemesis: "kill", Select: SelectOne},
			{Nemesis: "pause", Select: SelectMinority},
			{Nemesis: "partition-majority-minority", Select: SelectAll},
		},
		Duration:  time.Hour,
		MinGap:    10 * time.Second,
		MaxGap:    time.Minute,
		MinLength: 5 * time.Second,
		MaxLength: 30 * time.Second,
	}
}

func TestRandomScheduleIsReproducibleFromItsSeed(t *testing.T) {
	a, err := RandomSchedule(randomConfig(42))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RandomSchedule(randomConfig(42))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("the same seed drew different schedules")
	}
	c, err := RandomSchedule(randomConfig(43))
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(a.Windows, c.Windows) {
		t.Fatal("different seeds drew the same schedule")
	}
	if a.Seed != 42 || len(a.Windows) < 10 {
		t.Fatalf("seed %d, %d windows", a.Seed, len(a.Windows))
	}
}

func TestRandomScheduleWindowsRespectTheConfig(t *testing.T) {
	cfg := randomConfig(7)
	s, err := RandomSchedule(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var end time.Duration
	seen := map[string]bool{}
	for _, w := range s.Windows {
		gap := w.Start - end
		if gap < cfg.MinGap || gap > cfg.MaxGap || w.Length < cfg.MinLength || w.Length > cfg.MaxLength {
			t.Fatalf("window %+v outside the config (gap %v)", w, gap)
		}
		end = w.Start + w.Length
		seen[w.Nemesis] = true
		switch w.Nemesis {
		case "kill":
			if len(w.Targets) != 1 {
				t.Fatalf("kill targets %v", w.Targets)
			}
		case "pause":
			if len(w.Targets) < 1 || len(w.Targets) > 2 {
				t.Fatalf("pause targets %v", w.Targets)
			}
		default:
			if !slices.Equal(slices.Sorted(slices.Values(w.Targets)), cfg.Nodes) {
				t.Fatalf("partition targets %v", w.Targets)
			}
		}
	}
	if end > cfg.Duration || len(seen) != len(cfg.Choices) {
		t.Fatalf("schedule ends at %v, drew %v", end, seen)
	}
}

func TestRandomScheduleRejectsBadConfig(t *testing.T) {
	for name, mutate := range map[string]func(*RandomConfig){
		"no nodes":     func(c *RandomConfig) { c.Nodes = nil },
		"dup nodes":    func(c *RandomConfig) { c.Nodes = []string{"n1", "n1"} },
		"no choices":   func(c *RandomConfig) { c.Choices = nil },
		"no nemesis":   func(c *RandomConfig) { c.Choices = []Choice{{Select: SelectOne}} },
		"selector":     func(c *RandomConfig) { c.Choices = []Choice{{Nemesis: "kill", Select: "some"}} },
		"gap order":    func(c *RandomConfig) { c.MaxGap = c.MinGap - 1 },
		"negative gap": func(c *RandomConfig) { c.MinGap = -1 },
		"zero length":  func(c *RandomConfig) { c.MinLength = 0 },
		"length order": func(c *RandomConfig) { c.MaxLength = c.MinLength - 1 },
		"duration":     func(c *RandomConfig) { c.Duration = 0 },
	} {
		cfg := randomConfig(1)
		mutate(&cfg)
		var ce *ConfigError
		if _, err := RandomSchedule(cfg); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	cfg := randomConfig(1)
	cfg.Duration = time.Second
	if s, err := RandomSchedule(cfg); err != nil || len(s.Windows) != 0 {
		t.Fatalf("short run: %+v, %v", s, err)
	}
}

func TestFixedScheduleValidates(t *testing.T) {
	ok := []Window{
		{Nemesis: "kill", Targets: []string{"n1"}, Start: 0, Length: time.Second},
		{Nemesis: "pause", Targets: []string{"n2", "n3"}, Start: time.Second, Length: time.Second},
	}
	s, err := FixedSchedule(9, ok...)
	if err != nil || s.Seed != 9 || len(s.Windows) != 2 {
		t.Fatalf("FixedSchedule = %+v, %v", s, err)
	}
	for name, w := range map[string]Window{
		"no nemesis": {Targets: []string{"n1"}, Start: 5 * time.Second, Length: time.Second},
		"no targets": {Nemesis: "kill", Start: 5 * time.Second, Length: time.Second},
		"dup target": {Nemesis: "kill", Targets: []string{"n1", "n1"}, Start: 5 * time.Second, Length: time.Second},
		"length":     {Nemesis: "kill", Targets: []string{"n1"}, Start: 5 * time.Second},
		"overlap":    {Nemesis: "kill", Targets: []string{"n1"}, Start: time.Second, Length: time.Second},
	} {
		var ce *ConfigError
		if _, err := FixedSchedule(0, append(slices.Clone(ok), w)...); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

type eventLog struct{ events []Event }

func (l *eventLog) Record(e Event) { l.events = append(l.events, e) }

func (l *eventLog) shape() []string {
	var out []string
	for _, e := range l.events {
		out = append(out, string(e.Op)+":"+string(e.Phase))
	}
	return out
}

func newPlayer(log Recorder, nemeses ...*fakeNemesis) *Player {
	p := &Player{
		Nemeses:  map[string]Nemesis{},
		Targets:  map[string]Target{"n1": newFake("n1"), "n2": newFake("n2")},
		Recorder: log,
		Clock:    &fakeClock{now: time.Unix(1000, 0)},
	}
	for _, n := range nemeses {
		p.Nemeses[n.name] = n
	}
	return p
}

func twoWindows(t *testing.T) Schedule {
	t.Helper()
	s, err := FixedSchedule(5,
		Window{Nemesis: "a", Targets: []string{"n1"}, Start: 10 * time.Second, Length: 5 * time.Second},
		Window{Nemesis: "b", Targets: []string{"n2", "n1"}, Start: 20 * time.Second, Length: 5 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlayerRecordsEveryStepOnTheSchedule(t *testing.T) {
	log := &eventLog{}
	a, b := &fakeNemesis{name: "a"}, &fakeNemesis{name: "b"}
	p := newPlayer(log, a, b)
	s := twoWindows(t)
	if err := p.Play(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	want := []string{"schedule:ok", "inject:invoke", "inject:ok", "heal:invoke", "heal:ok", "inject:invoke", "inject:ok", "heal:invoke", "heal:ok"}
	if !reflect.DeepEqual(log.shape(), want) {
		t.Fatalf("events = %v", log.shape())
	}
	if first := log.events[0]; first.Schedule == nil || first.Schedule.Seed != 5 || first.Window != -1 {
		t.Fatalf("schedule event = %+v", first)
	}
	start := time.Unix(1000, 0)
	for i, at := range map[int]time.Duration{1: 10 * time.Second, 3: 15 * time.Second, 5: 20 * time.Second, 7: 25 * time.Second} {
		if got := log.events[i].Time.Sub(start); got != at {
			t.Errorf("event %d at %v, want %v", i, got, at)
		}
	}
	if !reflect.DeepEqual(b.injected, [][]string{{"n2", "n1"}}) || a.heals != 1 || b.heals != 1 {
		t.Fatalf("a %+v, b %+v", a, b)
	}
}

func TestPlayerHealsAndStopsAfterAFailedInject(t *testing.T) {
	log := &eventLog{}
	a, b := &fakeNemesis{name: "a", injectErr: errBoom}, &fakeNemesis{name: "b"}
	p := newPlayer(log, a, b)
	if err := p.Play(context.Background(), twoWindows(t)); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	want := []string{"schedule:ok", "inject:invoke", "inject:fail", "heal:invoke", "heal:ok"}
	if !reflect.DeepEqual(log.shape(), want) || log.events[2].Error == "" {
		t.Fatalf("events = %v", log.events)
	}
	// The heal follows the failed inject at once rather than holding a
	// fault that did not happen for the window's length.
	if log.events[3].Time != log.events[2].Time {
		t.Fatal("heal waited out the window after a failed inject")
	}
	if a.heals != 1 || len(b.injected) != 0 {
		t.Fatalf("a heals %d, b injected %v", a.heals, b.injected)
	}
}

func TestPlayerStopsAfterAFailedHeal(t *testing.T) {
	log := &eventLog{}
	a, b := &fakeNemesis{name: "a", healErr: errBoom}, &fakeNemesis{name: "b"}
	p := newPlayer(log, a, b)
	if err := p.Play(context.Background(), twoWindows(t)); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if got := log.shape(); got[len(got)-1] != "heal:fail" || len(b.injected) != 0 {
		t.Fatalf("events = %v", got)
	}
}

func TestPlayerHealsOnADetachedContextWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	log := &eventLog{}
	a := &fakeNemesis{name: "a", onInject: cancel}
	b := &fakeNemesis{name: "b"}
	p := newPlayer(log, a, b)
	if err := p.Play(ctx, twoWindows(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if a.heals != 1 || a.healCtx[0] != nil || len(b.injected) != 0 {
		t.Fatalf("a %+v, b %+v", a, b)
	}

	// Cancelled before the first window: nothing is injected.
	log = &eventLog{}
	a = &fakeNemesis{name: "a"}
	p = newPlayer(log, a, b)
	if err := p.Play(ctx, twoWindows(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(a.injected) != 0 || !reflect.DeepEqual(log.shape(), []string{"schedule:ok"}) {
		t.Fatalf("a %+v, events %v", a, log.shape())
	}
}

func TestPlayerResolvesEveryNameBeforeTheFirstFault(t *testing.T) {
	a := &fakeNemesis{name: "a"}
	for name, w := range map[string]Window{
		"nemesis": {Nemesis: "zzz", Targets: []string{"n1"}, Start: time.Hour, Length: time.Second},
		"target":  {Nemesis: "a", Targets: []string{"n9"}, Start: time.Hour, Length: time.Second},
	} {
		s, err := FixedSchedule(0, Window{Nemesis: "a", Targets: []string{"n1"}, Length: time.Second}, w)
		if err != nil {
			t.Fatal(err)
		}
		log := &eventLog{}
		var ce *ConfigError
		if err := newPlayer(log, a).Play(context.Background(), s); !errors.As(err, &ce) {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(a.injected) != 0 || len(log.events) != 0 {
			t.Fatalf("%s: a fault ran before resolution failed", name)
		}
	}
	var ce *ConfigError
	if err := newPlayer(nil, a).Play(context.Background(), Schedule{}); !errors.As(err, &ce) {
		t.Fatalf("no recorder: err = %v", err)
	}
	if err := newPlayer(&eventLog{}, a).Play(context.Background(), Schedule{Windows: []Window{{}}}); !errors.As(err, &ce) {
		t.Fatalf("invalid schedule: err = %v", err)
	}
}

func TestPlayerDefaults(t *testing.T) {
	var got []Event
	a := &fakeNemesis{name: "a"}
	p := &Player{
		Nemeses:  map[string]Nemesis{"a": a},
		Targets:  map[string]Target{"n1": newFake("n1")},
		Recorder: RecorderFunc(func(e Event) { got = append(got, e) }),
	}
	s, err := FixedSchedule(0, Window{Nemesis: "a", Targets: []string{"n1"}, Start: time.Millisecond, Length: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Play(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || a.heals != 1 || got[4].Time.Before(got[0].Time) {
		t.Fatalf("events = %+v", got)
	}
}
