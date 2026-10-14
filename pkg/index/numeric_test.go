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

package index

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/big"
	mrand "math/rand"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/jeremyhahn/go-qrdb/pkg/index/internal/indexkey"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// numericOracle is the exact value of a number's text, the reference the
// encoding's byte order is checked against: an infinity, or a rational.
type numericOracle struct {
	inf int // -1 for -Inf, +1 for +Inf, 0 for a finite value
	r   *big.Rat
}

func oracleOf(t *testing.T, s string) numericOracle {
	t.Helper()
	body := strings.TrimLeft(s, "+-")
	if strings.EqualFold(body, "inf") || strings.EqualFold(body, "infinity") {
		if strings.HasPrefix(s, "-") {
			return numericOracle{inf: -1}
		}
		return numericOracle{inf: 1}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("oracle cannot parse %q", s)
	}
	return numericOracle{r: r}
}

func (o numericOracle) cmp(p numericOracle) int {
	if o.inf != 0 || p.inf != 0 {
		switch {
		case o.inf < p.inf:
			return -1
		case o.inf > p.inf:
			return 1
		}
		if o.inf != 0 {
			return 0
		}
		// Exactly one side is finite and the other side's inf is 0 too,
		// which the switch already ordered; unreachable.
	}
	return o.r.Cmp(p.r)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func mustTerm(t *testing.T, s string) []byte {
	t.Helper()
	b, err := EncodeNumericValue(s)
	if err != nil {
		t.Fatalf("EncodeNumericValue(%q): %v", s, err)
	}
	return b
}

// numericCorpus returns a deterministic spread of number texts: the edges a
// float64 or an int64 encoding gets wrong, Go-formatted integers and floats,
// and arbitrary decimal texts with long digit strings and wide exponents.
func numericCorpus() []string {
	corpus := []string{
		"-Inf", "+Inf", "Inf", "-infinity", "0", "-0", "+0", "0.0", "-0.0e7",
		"1", "-1", "2", "10", "-10", "0.5", "-0.5", "0.49", "-0.49", "0.51",
		"9007199254740992", "9007199254740993", "9007199254740994",
		"-9007199254740993", "1700000000000000001", "1700000000000000002",
		strconv.FormatInt(math.MaxInt64, 10), strconv.FormatInt(math.MinInt64, 10),
		strconv.FormatUint(math.MaxUint64, 10), "18446744073709551616",
		"5e-324", "-5e-324", "1.7976931348623157e308", "-1.7976931348623157e308",
		"1e400", "-1e400", "1e-400", "100", "1e2", "0.1e3", "99.999", "100.001",
		".5", "5.", "1E+21", "1e21", "123456789012345678901234567890.5",
	}
	// #nosec G404 -- deterministic test fixture: the seed is fixed so the case is reproducible, and no key, nonce, token or identifier derives from these values.
	rng := rand.New(rand.NewPCG(35, 2026))
	for range 400 {
		switch rng.IntN(5) {
		case 0:
			corpus = append(corpus, strconv.FormatInt(rng.Int64()>>rng.IntN(63), 10))
			corpus = append(corpus, strconv.FormatInt(-(rng.Int64()>>rng.IntN(63)), 10))
		case 1:
			corpus = append(corpus, strconv.FormatUint(rng.Uint64()>>rng.IntN(64), 10))
		case 2:
			f := math.Float64frombits(rng.Uint64())
			if math.IsNaN(f) {
				continue
			}
			corpus = append(corpus, fmt.Sprint(f))
		case 3:
			corpus = append(corpus, fmt.Sprint(rng.NormFloat64()*math.Pow(10, float64(rng.IntN(40)-20))))
		default:
			corpus = append(corpus, randomDecimal(rng))
		}
	}
	return corpus
}

// randomDecimal writes a decimal text with leading and trailing zeros, an
// optional fraction and an optional exponent, so normalization is exercised.
func randomDecimal(rng *rand.Rand) string {
	var b strings.Builder
	if rng.IntN(2) == 0 {
		b.WriteByte('-')
	}
	b.WriteString(strings.Repeat("0", rng.IntN(3)))
	for range rng.IntN(25) + 1 {
		b.WriteByte(byte('0' + rng.IntN(10)))
	}
	if rng.IntN(2) == 0 {
		b.WriteByte('.')
		for range rng.IntN(25) {
			b.WriteByte(byte('0' + rng.IntN(10)))
		}
		b.WriteString(strings.Repeat("0", rng.IntN(3)))
	}
	if rng.IntN(2) == 0 {
		fmt.Fprintf(&b, "e%d", rng.IntN(600)-300)
	}
	return b.String()
}

// TestNumericTerm_OrderPreserving is the encoding's defining property: for
// every pair of numbers, the byte order of their terms is their numeric
// order -- including equality, so distinct numbers never share a term.
func TestNumericTerm_OrderPreserving(t *testing.T) {
	corpus := numericCorpus()
	terms := make([][]byte, len(corpus))
	oracles := make([]numericOracle, len(corpus))
	for i, s := range corpus {
		terms[i] = mustTerm(t, s)
		oracles[i] = oracleOf(t, s)
	}
	for i := range corpus {
		for j := range corpus {
			want := oracles[i].cmp(oracles[j])
			if got := sign(bytes.Compare(terms[i], terms[j])); got != want {
				t.Fatalf("compare(%q, %q): term order %d, numeric order %d", corpus[i], corpus[j], got, want)
			}
		}
	}
}

// TestNumericTerm_IndexKeyOrder checks the property an index key relies on:
// with a separator and any entity key after the term, keys still sort by
// value first. It holds because terms are prefix-free.
func TestNumericTerm_IndexKeyOrder(t *testing.T) {
	corpus := numericCorpus()
	entities := [][]byte{{}, {0x00}, []byte("a"), {0xFF, 0xFF}, []byte("zzz")}
	key := func(term, entity []byte) []byte {
		k := append(append([]byte{}, term...), indexkey.Separator...)
		return append(k, entity...)
	}
	for i := range corpus {
		ti := mustTerm(t, corpus[i])
		for j := range corpus {
			tj := mustTerm(t, corpus[j])
			c := sign(bytes.Compare(ti, tj))
			if c == 0 {
				continue
			}
			if len(ti) < len(tj) && bytes.Equal(ti, tj[:len(ti)]) {
				t.Fatalf("term of %q is a proper prefix of the term of %q", corpus[i], corpus[j])
			}
			for _, ei := range entities {
				for _, ej := range entities {
					if got := sign(bytes.Compare(key(ti, ei), key(tj, ej))); got != c {
						t.Fatalf("key order of %q/%q vs %q/%q is %d, want %d", corpus[i], ei, corpus[j], ej, got, c)
					}
				}
			}
		}
	}
}

// TestNumericTerm_Successor checks the exclusive-bound marker: the resolved
// bound sits above every key holding the value and at or below every key
// holding a greater value.
func TestNumericTerm_Successor(t *testing.T) {
	corpus := numericCorpus()
	for _, v := range corpus {
		succ, err := ResolveNumericScanBound([]byte(v+"\x00"), "n", "start")
		if err != nil {
			t.Fatalf("ResolveNumericScanBound(%q): %v", v, err)
		}
		tv := mustTerm(t, v)
		own := append(append(append([]byte{}, tv...), indexkey.Separator...), 0xFF, 0xFF, 0xFF)
		if bytes.Compare(own, succ) >= 0 {
			t.Fatalf("a key holding %q sorts at or above its successor bound", v)
		}
		ov := oracleOf(t, v)
		for _, w := range corpus {
			if oracleOf(t, w).cmp(ov) <= 0 {
				continue
			}
			tw := append(mustTerm(t, w), indexkey.Separator...)
			if bytes.Compare(tw, succ) < 0 {
				t.Fatalf("a key holding %q sorts below the successor of %q", w, v)
			}
		}
	}
}

// TestNumericTerm_QuickIntegers checks order and exactness over arbitrary
// int64 and uint64 pairs, the integers a float64 encoding rounds.
func TestNumericTerm_QuickIntegers(t *testing.T) {
	signed := func(a, b int64) bool {
		ta, errA := EncodeNumericValue(strconv.FormatInt(a, 10))
		tb, errB := EncodeNumericValue(strconv.FormatInt(b, 10))
		if errA != nil || errB != nil {
			return false
		}
		want := 0
		switch {
		case a < b:
			want = -1
		case a > b:
			want = 1
		}
		return sign(bytes.Compare(ta, tb)) == want
	}
	if err := quick.Check(signed, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
	unsigned := func(a, b uint64) bool {
		ta, errA := EncodeNumericValue(strconv.FormatUint(a, 10))
		tb, errB := EncodeNumericValue(strconv.FormatUint(b, 10))
		if errA != nil || errB != nil {
			return false
		}
		want := 0
		switch {
		case a < b:
			want = -1
		case a > b:
			want = 1
		}
		return sign(bytes.Compare(ta, tb)) == want
	}
	if err := quick.Check(unsigned, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
}

// TestNumericTerm_QuickFloats checks order over arbitrary float64 pairs as
// Go renders them (fmt.Sprint, the DAO's bound rendering), NaN excluded.
func TestNumericTerm_QuickFloats(t *testing.T) {
	prop := func(a, b float64) bool {
		if math.IsNaN(a) || math.IsNaN(b) {
			return true
		}
		ta, errA := EncodeNumericValue(fmt.Sprint(a))
		tb, errB := EncodeNumericValue(fmt.Sprint(b))
		if errA != nil || errB != nil {
			return false
		}
		want := 0
		switch {
		case a < b:
			want = -1
		case a > b:
			want = 1
		}
		return sign(bytes.Compare(ta, tb)) == want
	}
	// quick's own float64 generator yields only huge magnitudes; draw from
	// every bit pattern instead, with a share of small exact fractions so
	// equal and adjacent values occur.
	cfg := &quick.Config{
		MaxCount: 5000,
		Values: func(args []reflect.Value, rng *mrand.Rand) {
			for i := range args {
				f := math.Float64frombits(rng.Uint64())
				if rng.Intn(4) == 0 {
					f = float64(rng.Int63n(64)-32) / 8
				}
				args[i] = reflect.ValueOf(f)
			}
		},
	}
	if err := quick.Check(prop, cfg); err != nil {
		t.Fatal(err)
	}
}

// TestNumericTerm_RoundTrip checks that decoding a term yields text that
// encodes back to the same term.
func TestNumericTerm_RoundTrip(t *testing.T) {
	for _, s := range numericCorpus() {
		term := mustTerm(t, s)
		text, err := DecodeNumericValue(term)
		if err != nil {
			t.Fatalf("DecodeNumericValue(term of %q): %v", s, err)
		}
		if again := mustTerm(t, text); !bytes.Equal(again, term) {
			t.Fatalf("%q decoded to %q, which encodes to a different term", s, text)
		}
	}
}

func TestDecodeNumericValue_Canonical(t *testing.T) {
	tests := []struct{ in, want string }{
		{"123", "123"},
		{"-5", "-5"},
		{"0", "0"},
		{"-0", "0"},
		{"1.50", "1.5"},
		{"-0.25", "-0.25"},
		{"0.001", "0.001"},
		{"0.000001", "0.000001"},
		{"0.0000001", "1e-7"},
		{"1.5e-7", "1.5e-7"},
		{"1e20", "100000000000000000000"},
		{"1e21", "1e+21"},
		{"12.5e30", "1.25e+31"},
		{"9007199254740993", "9007199254740993"},
		{"18446744073709551615", "18446744073709551615"},
		{"inf", "+Inf"},
		{"-Infinity", "-Inf"},
	}
	for _, tt := range tests {
		got, err := DecodeNumericValue(mustTerm(t, tt.in))
		if err != nil {
			t.Fatalf("DecodeNumericValue(term of %q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("DecodeNumericValue(term of %q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestEncodeNumericValue_EqualValuesShareATerm pins exactness the other way:
// every spelling of one number, -0 included, is one term.
func TestEncodeNumericValue_EqualValuesShareATerm(t *testing.T) {
	groups := [][]string{
		{"0", "-0", "+0", "0.000", "-0e99", ".0"},
		{"1", "1.0", "+1", "10e-1", "0.1e1", "1.000", "001"},
		{"-2.5", "-25e-1", "-2.50", "-0.25E1"},
		{"Inf", "+inf", "INFINITY"},
	}
	for _, g := range groups {
		want := mustTerm(t, g[0])
		for _, s := range g[1:] {
			if got := mustTerm(t, s); !bytes.Equal(got, want) {
				t.Errorf("%q and %q are one number but encode differently", g[0], s)
			}
		}
	}
	if bytes.Equal(mustTerm(t, "9007199254740992"), mustTerm(t, "9007199254740993")) {
		t.Error("2^53 and 2^53+1 share a term; integers must be exact")
	}
}

func TestEncodeNumericValue_Rejects(t *testing.T) {
	tests := []struct {
		in     string
		reason NumberRejection
	}{
		{"", NumberSyntax},
		{"abc", NumberSyntax},
		{".", NumberSyntax},
		{"-", NumberSyntax},
		{"1e", NumberSyntax},
		{"1e+", NumberSyntax},
		{"--1", NumberSyntax},
		{"1.2.3", NumberSyntax},
		{"0x10", NumberSyntax},
		{"1_000", NumberSyntax},
		{" 1", NumberSyntax},
		{"1 ", NumberSyntax},
		{"NaN", NumberIsNaN},
		{"-nan", NumberIsNaN},
		{"1e2147483648", NumberExponentRange},
		{"1e-2147483650", NumberExponentRange},
		{"1e99999999999999999999999", NumberExponentRange},
	}
	for _, tt := range tests {
		_, err := EncodeNumericValue(tt.in)
		var ne *InvalidNumberError
		if !errors.As(err, &ne) {
			t.Fatalf("EncodeNumericValue(%q) = %v, want *InvalidNumberError", tt.in, err)
		}
		if ne.Reason != tt.reason || ne.Value != tt.in {
			t.Errorf("EncodeNumericValue(%q) rejected as %+v, want reason %q", tt.in, ne, tt.reason)
		}
		if !strings.Contains(err.Error(), string(tt.reason)) {
			t.Errorf("error text %q omits the reason", err.Error())
		}
	}
}

// TestEncodeNumericValue_ExponentEdges accepts the widest exponents that
// still fit, so the range check is not off by one.
func TestEncodeNumericValue_ExponentEdges(t *testing.T) {
	hi := mustTerm(t, "1e2147483646")
	lo := mustTerm(t, "1e-2147483648")
	if bytes.Compare(lo, hi) >= 0 || bytes.Compare(hi, mustTerm(t, "Inf")) >= 0 ||
		bytes.Compare(mustTerm(t, "0"), lo) >= 0 {
		t.Fatal("extreme exponents are out of order")
	}
}

func TestDecodeNumericValue_Corrupt(t *testing.T) {
	pos := mustTerm(t, "12.5")
	tests := []struct {
		name string
		term []byte
	}{
		{"empty", nil},
		{"unknown tag low", []byte{0x00}},
		{"unknown tag high", []byte{0x06}},
		{"trailing bytes", []byte{numericTagZero, 0x01}},
		{"truncated exponent", []byte{numericTagPos, 0x80, 0x00}},
		{"missing terminator", pos[:len(pos)-1]},
		{"invalid digit byte", []byte{numericTagPos, 0x80, 0, 0, 1, 111, 0}},
		{"lone digit before the end", []byte{numericTagPos, 0x80, 0, 0, 1, 12, 13, 0}},
		{"leading zero digit", []byte{numericTagPos, 0x80, 0, 0, 1, 1 + 0 + 6, 0}},
		{"trailing zero digit", []byte{numericTagPos, 0x80, 0, 0, 1, 1 + 11*5 + 1, 0}},
		{"no digits", []byte{numericTagPos, 0x80, 0, 0, 1, 0}},
	}
	for _, tt := range tests {
		_, err := DecodeNumericValue(tt.term)
		var ce *CorruptNumericTermError
		if !errors.As(err, &ce) {
			t.Errorf("%s: DecodeNumericValue = %v, want *CorruptNumericTermError", tt.name, err)
			continue
		}
		if !strings.Contains(ce.Error(), ce.Reason) {
			t.Errorf("%s: error text %q omits the reason", tt.name, ce.Error())
		}
	}
}

func TestResolveNumericScanBound_RefusesNaNAndGarbage(t *testing.T) {
	for _, raw := range [][]byte{[]byte("NaN"), []byte("NaN\x00"), []byte("x"), {0x00}} {
		_, err := ResolveNumericScanBound(raw, "n", "end")
		var be *InvalidNumericBoundError
		if !errors.As(err, &be) {
			t.Fatalf("ResolveNumericScanBound(%q) = %v, want *InvalidNumericBoundError", raw, err)
		}
		var ne *InvalidNumberError
		if !errors.As(err, &ne) {
			t.Fatalf("ResolveNumericScanBound(%q) does not wrap *InvalidNumberError: %v", raw, err)
		}
	}
}

func TestNormalizeFieldValue_NumericSkipsNaN(t *testing.T) {
	cfg := IndexConfig{ValueType: IndexValueTypeNumeric}
	if _, ok := NormalizeFieldValue([]byte("NaN"), cfg); ok {
		t.Error("NaN was indexed; it has no place in the order")
	}
	if got, ok := NormalizeFieldValue([]byte("-0"), cfg); !ok || !bytes.Equal(got, mustTerm(t, "0")) {
		t.Errorf("-0 normalized to %x, %v; want the term of 0", got, ok)
	}
}

func TestDecodeFieldValue_Numeric(t *testing.T) {
	cfg := IndexConfig{ValueType: IndexValueTypeNumeric}
	if got := DecodeFieldValue(mustTerm(t, "-1.25"), cfg); got != "-1.25" {
		t.Errorf("DecodeFieldValue = %q, want -1.25", got)
	}
	if got := DecodeFieldValue([]byte("junk"), cfg); got != "junk" {
		t.Errorf("DecodeFieldValue of a non-term = %q, want the raw bytes", got)
	}
}

func TestIndexConfig_CheckEncoding(t *testing.T) {
	tests := []struct {
		name  string
		cfg   IndexConfig
		stale bool
	}{
		{"string ignores version", IndexConfig{Name: "s"}, false},
		{"numeric current", IndexConfig{Name: "n", ValueType: IndexValueTypeNumeric, EncodingVersion: NumericEncodingVersion}, false},
		{"numeric unversioned", IndexConfig{Name: "n", ValueType: IndexValueTypeNumeric}, true},
		{"numeric future", IndexConfig{Name: "n", ValueType: IndexValueTypeNumeric, EncodingVersion: NumericEncodingVersion + 1}, true},
		{"compound numeric member", IndexConfig{Name: "c", ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeNumeric}}, true},
		{"compound strings", IndexConfig{Name: "c", ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString}}, false},
	}
	for _, tt := range tests {
		err := tt.cfg.CheckEncoding()
		var se *StaleEncodingError
		if got := errors.As(err, &se); got != tt.stale {
			t.Errorf("%s: CheckEncoding = %v, stale want %v", tt.name, err, tt.stale)
			continue
		}
		if tt.stale && (se.IndexName != tt.cfg.Name || se.Version != tt.cfg.EncodingVersion ||
			!strings.Contains(se.Error(), "rebuild")) {
			t.Errorf("%s: StaleEncodingError = %+v (%q)", tt.name, se, se.Error())
		}
	}
}

// TestNumericIndex_Constructors_RefuseStaleEncoding pins the coordination
// point with a persisted definition: a numeric index recorded at another
// encoding version is never constructed, so it cannot be served. The
// pebbleindex package pins the same for its constructor.
func TestNumericIndex_Constructors_RefuseStaleEncoding(t *testing.T) {
	stale := IndexConfig{Name: "n", ValueType: IndexValueTypeNumeric}
	var se *StaleEncodingError

	stg, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebbleStorage.New: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	if _, err := NewStoragePebbleIndex(stg, stale, testLogger(t)); !errors.As(err, &se) {
		t.Errorf("NewStoragePebbleIndex = %v, want *StaleEncodingError", err)
	}
	compound := IndexConfig{
		Name:       "c",
		FieldNames: []string{"a", "b"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeNumeric},
	}
	if _, err := NewCompoundIndex(stg, compound, testLogger(t)); !errors.As(err, &se) {
		t.Errorf("NewCompoundIndex = %v, want *StaleEncodingError", err)
	}
	compound.EncodingVersion = NumericEncodingVersion
	if _, err := NewCompoundIndex(stg, compound, testLogger(t)); err != nil {
		t.Errorf("NewCompoundIndex at the current version: %v", err)
	}
}

func TestDecodeCompoundValue_NumericTerms(t *testing.T) {
	types := []IndexValueType{IndexValueTypeNumeric, IndexValueTypeString, IndexValueTypeNumeric, IndexValueTypeNumeric}
	values := [][]byte{mustTerm(t, "-12.5"), []byte("x"), mustTerm(t, "0"), mustTerm(t, "-Inf")}
	decoded := DecodeCompoundValue(EncodeCompoundValue(values, types), types)
	if len(decoded) != len(values) {
		t.Fatalf("decoded %d fields, want %d", len(decoded), len(values))
	}
	for i := range values {
		if !bytes.Equal(decoded[i], values[i]) {
			t.Errorf("field %d = %x, want %x", i, decoded[i], values[i])
		}
	}

	// A malformed numeric member ends decoding like truncated data.
	bad := append([]byte{0x7F}, mustTerm(t, "1")...)
	if got := DecodeCompoundValue(bad, types[:1]); len(got) != 0 {
		t.Errorf("malformed numeric member decoded to %x", got)
	}
}

// TestCompoundValue_Fixed64 pins the fixed-width member SQL primary keys are
// stored under: its bytes pass through unchanged, split back at eight bytes
// even when they contain the string terminator, and a short tail ends decoding.
func TestCompoundValue_Fixed64(t *testing.T) {
	types := []IndexValueType{IndexValueTypeFixed64, IndexValueTypeString, IndexValueTypeFixed64}
	values := [][]byte{
		{0x80, 0, 0, 0, 0, 0, 0, 0x2A},
		[]byte("a\x00b"),
		{0, 0, 0, 0, 0, 0, 0, 0},
	}
	encoded := EncodeCompoundValue(values, types)
	want := append(append(append([]byte{}, values[0]...), "a\x00\xffb\x00\x00"...), values[2]...)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("EncodeCompoundValue = %x, want %x", encoded, want)
	}
	decoded := DecodeCompoundValue(encoded, types)
	if len(decoded) != len(values) {
		t.Fatalf("decoded %d fields, want %d", len(decoded), len(values))
	}
	for i := range values {
		if !bytes.Equal(decoded[i], values[i]) {
			t.Errorf("field %d = %x, want %x", i, decoded[i], values[i])
		}
	}

	if got := DecodeCompoundValue(values[0][:7], types[:1]); len(got) != 0 {
		t.Errorf("a seven-byte fixed member decoded to %x", got)
	}
}
