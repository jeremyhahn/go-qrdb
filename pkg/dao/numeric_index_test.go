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

package dao

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// numericMember has indexed numeric fields of both an integer and a float
// kind, and an indexed string field for the bound-exactness controls.
type numericMember struct {
	ID    uint64  `json:"id"`
	Name  string  `json:"name" index:"true"`
	Age   int     `json:"age" index:"true"`
	Score float64 `json:"score" index:"true"`
}

// EntityID implements Entity.
func (m *numericMember) EntityID() uint64 { return m.ID }

// SetEntityID implements Entity.
func (m *numericMember) SetEntityID(id uint64) { m.ID = id }

// indexOnlyStore is a real in-memory store that can be sealed against full
// scans, so a query answered after sealing was answered by its index. It
// counts index range scans, so a test can also prove the index was NOT used.
type indexOnlyStore struct {
	*kvstore.MemoryKVStore
	sealed     atomic.Bool
	indexScans atomic.Int64
}

// errFullScan is what a sealed indexOnlyStore answers a prefix scan with.
var errFullScan = errors.New("indexOnlyStore: full scan refused")

func (s *indexOnlyStore) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	if s.sealed.Load() {
		return errFullScan
	}
	return s.MemoryKVStore.Scan(ctx, prefix, fn)
}

func (s *indexOnlyStore) ScanIndex(ctx context.Context, name string, start, end []byte) (map[string][][]byte, error) {
	s.indexScans.Add(1)
	return s.MemoryKVStore.ScanIndex(ctx, name, start, end)
}

// newNumericDAO returns a DAO over an indexOnlyStore seeded with members.
// The store is NOT sealed; a test seals it when it needs index-only answers.
func newNumericDAO(t *testing.T, members ...*numericMember) (GenericDAO[*numericMember], *indexOnlyStore) {
	t.Helper()
	store := &indexOnlyStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	d, err := New(store, "member", func() *numericMember { return &numericMember{} })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, m := range members {
		if err := d.Save(context.Background(), m); err != nil {
			t.Fatalf("Save(%s): %v", m.Name, err)
		}
	}
	return d, store
}

// memberNames returns the sorted names of members, the comparable form of a
// query result.
func memberNames(members []*numericMember) []string {
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	slices.Sort(names)
	return names
}

// TestDAO_NumericRangeReturnsBothTwoAndTen is the acceptance case: with an
// indexed numeric field holding 2 and 10, `>= 2` answers BOTH records, from
// the index. Decimal text puts "10" before "2", so a text-ordered scan from
// "2" returned the record holding 2 alone, with a nil error.
func TestDAO_NumericRangeReturnsBothTwoAndTen(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "two", Age: 2},
		&numericMember{Name: "ten", Age: 10},
	)
	store.sealed.Store(true)

	got, err := d.Find(context.Background()).Where("Age >= ?", 2).Execute()
	if err != nil {
		t.Fatalf("Age >= 2: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Age >= 2 returned no records; want two and ten")
	}
	if names := memberNames(got); !slices.Equal(names, []string{"ten", "two"}) {
		t.Errorf("Age >= 2 = %v; want [ten two]", names)
	}
	if store.indexScans.Load() == 0 {
		t.Error("Age >= 2 did not scan the Age index")
	}
}

// TestDAO_NumericIntegerRangeIsCompleteAndExact covers every comparison
// operator over integers of both signs, each answered by the index alone.
func TestDAO_NumericIntegerRangeIsCompleteAndExact(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "neg", Age: -5},
		&numericMember{Name: "zero", Age: 0},
		&numericMember{Name: "two", Age: 2},
		&numericMember{Name: "ten", Age: 10},
		&numericMember{Name: "hundred", Age: 100},
	)
	store.sealed.Store(true)

	for _, tc := range []struct {
		expr string
		args []interface{}
		want []string
	}{
		{"Age >= ?", []interface{}{2}, []string{"hundred", "ten", "two"}},
		{"Age > ?", []interface{}{2}, []string{"hundred", "ten"}},
		{"Age < ?", []interface{}{10}, []string{"neg", "two", "zero"}},
		{"Age <= ?", []interface{}{10}, []string{"neg", "ten", "two", "zero"}},
		{"Age >= ?", []interface{}{-5}, []string{"hundred", "neg", "ten", "two", "zero"}},
		{"Age > ?", []interface{}{-5}, []string{"hundred", "ten", "two", "zero"}},
		{"Age < ?", []interface{}{0}, []string{"neg"}},
		{"Age <= ?", []interface{}{-6}, []string{}},
		{"Age BETWEEN ? AND ?", []interface{}{2, 10}, []string{"ten", "two"}},
		{"Age BETWEEN ? AND ?", []interface{}{-10, 1}, []string{"neg", "zero"}},
		{"Age = ?", []interface{}{10}, []string{"ten"}},
	} {
		t.Run(tc.expr+" "+strconv.Itoa(len(tc.want)), func(t *testing.T) {
			got, err := d.Find(context.Background()).Where(tc.expr, tc.args...).Execute()
			if err != nil {
				t.Fatalf("%s %v: %v", tc.expr, tc.args, err)
			}
			if names := memberNames(got); !slices.Equal(names, tc.want) {
				t.Errorf("%s %v = %v; want %v", tc.expr, tc.args, names, tc.want)
			}
		})
	}
}

// TestDAO_NumericFloatRangeIsCompleteAndExact covers fractions, negatives,
// an exponent beyond any integer's text width, and negative zero, which
// orders as zero.
func TestDAO_NumericFloatRangeIsCompleteAndExact(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "neg", Score: -1.5},
		&numericMember{Name: "negzero", Score: math.Copysign(0, -1)},
		&numericMember{Name: "frac", Score: 2.25},
		&numericMember{Name: "huge", Score: 1e21},
	)
	store.sealed.Store(true)

	for _, tc := range []struct {
		expr string
		args []interface{}
		want []string
	}{
		{"Score >= ?", []interface{}{0.0}, []string{"frac", "huge", "negzero"}},
		{"Score > ?", []interface{}{0.0}, []string{"frac", "huge"}},
		{"Score < ?", []interface{}{0.0}, []string{"neg"}},
		{"Score <= ?", []interface{}{math.Copysign(0, -1)}, []string{"neg", "negzero"}},
		{"Score <= ?", []interface{}{2.25}, []string{"frac", "neg", "negzero"}},
		{"Score > ?", []interface{}{2.25}, []string{"huge"}},
		{"Score >= ?", []interface{}{1e20}, []string{"huge"}},
		{"Score BETWEEN ? AND ?", []interface{}{-2.0, 3.0}, []string{"frac", "neg", "negzero"}},
		{"Score > ?", []interface{}{math.Inf(-1)}, []string{"frac", "huge", "neg", "negzero"}},
		{"Score < ?", []interface{}{math.Inf(1)}, []string{"frac", "huge", "neg", "negzero"}},
	} {
		t.Run(tc.expr+" "+strconv.Itoa(len(tc.want)), func(t *testing.T) {
			got, err := d.Find(context.Background()).Where(tc.expr, tc.args...).Execute()
			if err != nil {
				t.Fatalf("%s %v: %v", tc.expr, tc.args, err)
			}
			if names := memberNames(got); !slices.Equal(names, tc.want) {
				t.Errorf("%s %v = %v; want %v", tc.expr, tc.args, names, tc.want)
			}
		})
	}
}

// TestDAO_NumericRangeMatchesBruteForce is the property at the DAO's level:
// for random integers and random bounds, every operator's index answer is
// exactly the set a comparison over every record selects.
func TestDAO_NumericRangeMatchesBruteForce(t *testing.T) {
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewPCG(35, 2026))
	members := make([]*numericMember, 120)
	for i := range members {
		members[i] = &numericMember{Name: strconv.Itoa(i), Age: rng.IntN(2001) - 1000}
	}
	d, store := newNumericDAO(t, members...)
	store.sealed.Store(true)

	ops := map[string]func(age, bound int) bool{
		"Age >= ?": func(a, b int) bool { return a >= b },
		"Age > ?":  func(a, b int) bool { return a > b },
		"Age < ?":  func(a, b int) bool { return a < b },
		"Age <= ?": func(a, b int) bool { return a <= b },
	}
	for range 40 {
		// Half the bounds are values a record holds, where inclusivity shows.
		bound := rng.IntN(2201) - 1100
		if rng.IntN(2) == 0 {
			bound = members[rng.IntN(len(members))].Age
		}
		for expr, match := range ops {
			want := []string{}
			for _, m := range members {
				if match(m.Age, bound) {
					want = append(want, m.Name)
				}
			}
			slices.Sort(want)

			got, err := d.Find(context.Background()).Where(expr, bound).Execute()
			if err != nil {
				t.Fatalf("%s %d: %v", expr, bound, err)
			}
			if names := memberNames(got); !slices.Equal(names, want) {
				t.Fatalf("%s %d returned %d records; brute force selects %d",
					expr, bound, len(names), len(want))
			}
		}
	}
}

// TestDAO_NumericEqualityUsesTheIndex covers the exact-match paths, planned
// and direct, which encode the queried value the way the stored one was.
func TestDAO_NumericEqualityUsesTheIndex(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "two", Age: 2, Score: 0.5},
		&numericMember{Name: "ten", Age: 10, Score: math.Copysign(0, -1)},
	)
	store.sealed.Store(true)
	ctx := context.Background()

	planned, err := d.Find(ctx).Where("Age = ?", 10).Execute()
	if err != nil {
		t.Fatalf("planned equality: %v", err)
	}
	if names := memberNames(planned); !slices.Equal(names, []string{"ten"}) {
		t.Errorf("Age = 10 = %v; want [ten]", names)
	}

	direct, err := d.QueryByIndex(ctx, "Age", 10)
	if err != nil {
		t.Fatalf("QueryByIndex: %v", err)
	}
	if names := memberNames(direct); !slices.Equal(names, []string{"ten"}) {
		t.Errorf("QueryByIndex(Age, 10) = %v; want [ten]", names)
	}

	// Negative zero was stored; zero finds it, because they are one number.
	zero, err := d.QueryByIndex(ctx, "Score", 0)
	if err != nil {
		t.Fatalf("QueryByIndex(Score, 0): %v", err)
	}
	if names := memberNames(zero); !slices.Equal(names, []string{"ten"}) {
		t.Errorf("QueryByIndex(Score, 0) = %v; want [ten]", names)
	}
}

// TestDAO_NumericScanByIndexIsNumericallyOrdered covers the direct range
// path, whose bounds are the half-open interval [start, end).
func TestDAO_NumericScanByIndexIsNumericallyOrdered(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "two", Age: 2},
		&numericMember{Name: "ten", Age: 10},
		&numericMember{Name: "hundred", Age: 100},
	)
	store.sealed.Store(true)

	got, err := d.ScanByIndex(context.Background(), "Age", 2, 100)
	if err != nil {
		t.Fatalf("ScanByIndex: %v", err)
	}
	if names := memberNames(got); !slices.Equal(names, []string{"ten", "two"}) {
		t.Errorf("ScanByIndex(Age, 2, 100) = %v; want [ten two]", names)
	}

	open, err := d.ScanByIndex(context.Background(), "Age", 3, nil)
	if err != nil {
		t.Fatalf("ScanByIndex open end: %v", err)
	}
	if names := memberNames(open); !slices.Equal(names, []string{"hundred", "ten"}) {
		t.Errorf("ScanByIndex(Age, 3, nil) = %v; want [hundred ten]", names)
	}
}

// TestDAO_NumericNaNBoundIsRefused proves NaN, which has no place in the
// numeric order, is refused as a bound with the typed error rather than
// answered as an empty range.
func TestDAO_NumericNaNBoundIsRefused(t *testing.T) {
	d, store := newNumericDAO(t, &numericMember{Name: "frac", Score: 2.25})
	store.sealed.Store(true)

	_, err := d.Find(context.Background()).Where("Score >= ?", math.NaN()).Execute()
	var bound *index.InvalidNumericBoundError
	if !errors.As(err, &bound) {
		t.Fatalf("Score >= NaN error = %v; want *index.InvalidNumericBoundError", err)
	}

	if _, err := d.ScanByIndex(context.Background(), "Score", "not-a-number", nil); !errors.As(err, &bound) {
		t.Errorf("ScanByIndex(text bound) error = %v; want *index.InvalidNumericBoundError", err)
	}
}

// TestDAO_NumericPrefixIsNotPlannedOnTheIndex proves a PREFIX predicate on a
// numeric field never reaches the index: its terms are numbers, not text, so
// they hold no text prefix to scan. The query runs as a full scan.
func TestDAO_NumericPrefixIsNotPlannedOnTheIndex(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "one", Age: 1},
		&numericMember{Name: "twelve", Age: 12},
	)

	if _, err := d.Find(context.Background()).Where("Age PREFIX ?", "1").Execute(); err != nil {
		t.Fatalf("Age PREFIX 1: %v", err)
	}
	if n := store.indexScans.Load(); n != 0 {
		t.Errorf("Age PREFIX 1 scanned an index %d times; want a full scan", n)
	}
}

// TestDAO_StringRangeBoundsAreExact covers the inclusivity the planner's
// range bounds carry on a text index: the consumed predicate is not
// re-checked, so `>` must exclude the bound and `<=` must include it.
func TestDAO_StringRangeBoundsAreExact(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "alpha"},
		&numericMember{Name: "bravo"},
		&numericMember{Name: "charlie"},
	)
	store.sealed.Store(true)

	for _, tc := range []struct {
		expr string
		args []interface{}
		want []string
	}{
		{"Name > ?", []interface{}{"bravo"}, []string{"charlie"}},
		{"Name >= ?", []interface{}{"bravo"}, []string{"bravo", "charlie"}},
		{"Name < ?", []interface{}{"bravo"}, []string{"alpha"}},
		{"Name <= ?", []interface{}{"bravo"}, []string{"alpha", "bravo"}},
		{"Name BETWEEN ? AND ?", []interface{}{"alpha", "bravo"}, []string{"alpha", "bravo"}},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := d.Find(context.Background()).Where(tc.expr, tc.args...).Execute()
			if err != nil {
				t.Fatalf("%s %v: %v", tc.expr, tc.args, err)
			}
			if names := memberNames(got); !slices.Equal(names, tc.want) {
				t.Errorf("%s %v = %v; want %v", tc.expr, tc.args, names, tc.want)
			}
		})
	}
}

// A prefix scan starts at the prefix and runs to the end of the index, so the
// values past it ("charlie", "bz" > "b") must be trimmed by the post-filter.
func TestDAO_StringPrefixIsExact(t *testing.T) {
	d, store := newNumericDAO(t,
		&numericMember{Name: "alpha"},
		&numericMember{Name: "b"},
		&numericMember{Name: "bravo"},
		&numericMember{Name: "bz"},
		&numericMember{Name: "charlie"},
	)
	store.sealed.Store(true)

	got, err := d.Find(context.Background()).Where("Name PREFIX ?", "br").Execute()
	if err != nil {
		t.Fatalf("Name PREFIX br: %v", err)
	}
	if names := memberNames(got); !slices.Equal(names, []string{"bravo"}) {
		t.Errorf("Name PREFIX br = %v; want [bravo]", names)
	}
	if store.indexScans.Load() == 0 {
		t.Error("Name PREFIX br did not scan the Name index")
	}

	got, err = d.Find(context.Background()).Where("Name PREFIX ?", "b").Execute()
	if err != nil {
		t.Fatalf("Name PREFIX b: %v", err)
	}
	if names := memberNames(got); !slices.Equal(names, []string{"b", "bravo", "bz"}) {
		t.Errorf("Name PREFIX b = %v; want [b bravo bz]", names)
	}
}
