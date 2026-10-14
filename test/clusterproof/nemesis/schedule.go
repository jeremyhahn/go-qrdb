//go:build clusterproof

package nemesis

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"time"
)

// Window is one fault in a schedule: a nemesis, the nodes it acts on, and
// when it is injected and for how long, relative to the start of the run.
type Window struct {
	Nemesis string        `json:"nemesis"`
	Targets []string      `json:"targets"`
	Start   time.Duration `json:"start"`
	Length  time.Duration `json:"length"`
}

// Schedule is a sequence of non-overlapping fault windows in start order.
// Seed is the seed a random schedule was drawn from (zero for a fixed one);
// the schedule itself is recorded in the history, so a run can be replayed
// from the record even where the seed alone would not reproduce it.
type Schedule struct {
	Seed    int64    `json:"seed"`
	Windows []Window `json:"windows"`
}

// FixedSchedule returns the schedule of the given windows, for example a
// recorded schedule replayed.
func FixedSchedule(seed int64, windows ...Window) (Schedule, error) {
	s := Schedule{Seed: seed, Windows: windows}
	return s, s.Validate()
}

// Validate checks that every window names a nemesis and distinct targets,
// has a positive length, and starts after the previous one has healed.
func (s Schedule) Validate() error {
	var end time.Duration
	for i, w := range s.Windows {
		field := "schedule window " + strconv.Itoa(i)
		switch {
		case w.Nemesis == "":
			return &ConfigError{Field: field, Reason: "names no nemesis"}
		case len(w.Targets) == 0:
			return &ConfigError{Field: field, Reason: "names no targets"}
		case w.Length <= 0:
			return &ConfigError{Field: field, Reason: "length must be positive"}
		case w.Start < end:
			return &ConfigError{Field: field, Reason: "starts before the previous window ends"}
		}
		sorted := slices.Sorted(slices.Values(w.Targets))
		if len(slices.Compact(sorted)) != len(w.Targets) {
			return &ConfigError{Field: field, Reason: "names a target twice"}
		}
		end = w.Start + w.Length
	}
	return nil
}

// Selector chooses the targets of a random window.
type Selector string

const (
	// SelectOne picks one node.
	SelectOne Selector = "one"
	// SelectMinority picks between one node and a minority of them.
	SelectMinority Selector = "minority"
	// SelectAll picks every node, in a random order: the order a partition's
	// grudge splits on.
	SelectAll Selector = "all"
)

var selectors = map[Selector]func(rng *rand.Rand, nodes []string) []string{
	SelectOne: func(rng *rand.Rand, nodes []string) []string {
		return []string{nodes[rng.IntN(len(nodes))]}
	},
	SelectMinority: func(rng *rand.Rand, nodes []string) []string {
		k := 1 + rng.IntN(max(1, (len(nodes)-1)/2))
		return shuffled(rng, nodes)[:k]
	},
	SelectAll: shuffled,
}

func shuffled(rng *rand.Rand, nodes []string) []string {
	s := slices.Clone(nodes)
	rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	return s
}

// Choice is one fault a random schedule may draw, and how it picks targets.
type Choice struct {
	Nemesis string
	Select  Selector
}

// RandomConfig configures a random schedule.
type RandomConfig struct {
	Seed    int64
	Nodes   []string
	Choices []Choice
	// Duration is the run every window must fit in.
	Duration time.Duration
	// Each window is preceded by a quiet gap in [MinGap, MaxGap] and lasts
	// a length in [MinLength, MaxLength].
	MinGap, MaxGap       time.Duration
	MinLength, MaxLength time.Duration
}

// RandomSchedule draws a schedule from cfg. The same seed and config give
// the same schedule under the same toolchain.
func RandomSchedule(cfg RandomConfig) (Schedule, error) {
	if err := cfg.validate(); err != nil {
		return Schedule{}, err
	}
	// A seeded generator is the point: the schedule must be reproducible
	// from its recorded seed, and nothing secret is drawn from it.
	seed := uint64(cfg.Seed)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // G404: see the note above
	s := Schedule{Seed: cfg.Seed}
	var at time.Duration
	for {
		start := at + between(rng, cfg.MinGap, cfg.MaxGap)
		length := between(rng, cfg.MinLength, cfg.MaxLength)
		if start+length > cfg.Duration {
			break
		}
		c := cfg.Choices[rng.IntN(len(cfg.Choices))]
		s.Windows = append(s.Windows, Window{
			Nemesis: c.Nemesis,
			Targets: selectors[c.Select](rng, cfg.Nodes),
			Start:   start,
			Length:  length,
		})
		at = start + length
	}
	return s, s.Validate()
}

func (cfg RandomConfig) validate() error {
	switch {
	case len(cfg.Nodes) == 0:
		return &ConfigError{Field: "random schedule nodes", Reason: "must not be empty"}
	case len(cfg.Choices) == 0:
		return &ConfigError{Field: "random schedule choices", Reason: "must not be empty"}
	case cfg.MinGap < 0 || cfg.MaxGap < cfg.MinGap:
		return &ConfigError{Field: "random schedule gap", Reason: "needs 0 <= min <= max"}
	case cfg.MinLength <= 0 || cfg.MaxLength < cfg.MinLength:
		return &ConfigError{Field: "random schedule length", Reason: "needs 0 < min <= max"}
	case cfg.Duration <= 0:
		return &ConfigError{Field: "random schedule duration", Reason: "must be positive"}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(cfg.Nodes)))) != len(cfg.Nodes) {
		return &ConfigError{Field: "random schedule nodes", Reason: "names a node twice"}
	}
	for _, c := range cfg.Choices {
		if c.Nemesis == "" {
			return &ConfigError{Field: "random schedule choice", Reason: "names no nemesis"}
		}
		if _, ok := selectors[c.Select]; !ok {
			return &ConfigError{Field: "random schedule choice " + c.Nemesis, Reason: "unknown selector " + strconv.Quote(string(c.Select))}
		}
	}
	return nil
}

// between draws a duration in [lo, hi].
func between(rng *rand.Rand, lo, hi time.Duration) time.Duration {
	return lo + time.Duration(rng.Int64N(int64(hi-lo)+1))
}
