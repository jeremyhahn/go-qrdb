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

package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// oversizedKey is one byte past what a two-byte length field can describe.
// Written into such a field by a narrow cast it becomes length 1, so the
// decoder reads a ONE-BYTE key and folds the remaining 65,536 bytes into the
// next field — storing under, or deleting, a different record than the caller
// named.
func oversizedKey() []byte { return bytes.Repeat([]byte("k"), MaxUint16Field+2) }

// oversizedTags builds a tag map with more entries than a two-byte COUNT
// field can describe. Counts overflow exactly as byte lengths do, and the
// consequence is the same class: the decoder reads fewer tags than were
// written and treats the rest as whatever follows.
func oversizedTags() map[string]string {
	tags := make(map[string]string, MaxUint16Field+2)
	for i := 0; i <= MaxUint16Field+1; i++ {
		tags["t"+string(rune(i%1000))+string(rune(i/1000))] = "v"
	}
	return tags
}

// Every encoder that frames a KEY in a two-byte field refuses a key that
// field cannot describe, and names the field in the error.
func TestEncoders_RefuseAnUnencodableKey(t *testing.T) {
	key := oversizedKey()

	for _, tc := range []struct {
		name  string
		field string
		call  func() ([]byte, error)
	}{
		{"EncodeMVCCPut", "key", func() ([]byte, error) { return EncodeMVCCPut(key, []byte("v"), 0) }},
		{"EncodeMVCCPutIfAbsent", "key", func() ([]byte, error) { return EncodeMVCCPutIfAbsent(key, []byte("v"), 0) }},
		{"EncodeMVCCDelete", "key", func() ([]byte, error) { return EncodeMVCCDelete(key) }},
		{"EncodeCompareAndDelete", "key", func() ([]byte, error) { return EncodeCompareAndDelete(key, []byte("v")) }},
		{"EncodeMVCCRange/start", "range start", func() ([]byte, error) { return EncodeMVCCRange(key, nil, 0, 0, false, false) }},
		{"EncodeMVCCRange/end", "range end", func() ([]byte, error) { return EncodeMVCCRange(nil, key, 0, 0, false, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.call()
			if err == nil {
				t.Fatalf("encoding a %d-byte key succeeded; it would decode as a "+
					"%d-byte key and address the wrong record",
					len(key), len(key)%(MaxUint16Field+1))
			}
			if cmd != nil {
				t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
			}
			var tooLong *FieldTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
			}
			if tooLong.Field != tc.field {
				t.Fatalf("Field = %q, want %q — the error must name the field that "+
					"overflowed", tooLong.Field, tc.field)
			}
			if tooLong.Length != len(key) || tooLong.Max != MaxUint16Field {
				t.Fatalf("error = %+v, want Length=%d Max=%d", tooLong, len(key), MaxUint16Field)
			}
		})
	}
}

// The index and time-series encoders frame NAMES and METRICS in two-byte
// fields, and are refused on the same terms.
func TestEncoders_RefuseAnUnencodableName(t *testing.T) {
	long := strings.Repeat("n", MaxUint16Field+2)

	for _, tc := range []struct {
		name  string
		field string
		call  func() ([]byte, error)
	}{
		{"EncodeIndexCreate/name", "index name", func() ([]byte, error) { return EncodeIndexCreate(long, "f", false, false, false) }},
		{"EncodeIndexCreate/field", "index field", func() ([]byte, error) { return EncodeIndexCreate("n", long, false, false, false) }},
		{"EncodeIndexDrop", "index name", func() ([]byte, error) { return EncodeIndexDrop(long) }},
		{"EncodeTSWrite", "metric", func() ([]byte, error) { return EncodeTSWrite(long, 0, 0, nil) }},
		{"EncodeTSWritePoint", "metric", func() ([]byte, error) { return EncodeTSWritePoint(long, 0, 0, nil) }},
		{"EncodeTSDeleteSeries", "metric", func() ([]byte, error) { return EncodeTSDeleteSeries(long, 0, 0, nil) }},
		{"EncodeTSSetRetention", "metric", func() ([]byte, error) { return EncodeTSSetRetention(long, 0, 0, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.call()
			if err == nil {
				t.Fatalf("encoding a %d-byte %s succeeded", len(long), tc.field)
			}
			if cmd != nil {
				t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
			}
			var tooLong *FieldTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
			}
			if tooLong.Field != tc.field {
				t.Fatalf("Field = %q, want %q", tooLong.Field, tc.field)
			}
		})
	}
}

// A tag COUNT overflows the same way a byte length does, and the decoder's
// misreading is the same class of defect.
func TestEncodeTSWrite_RefusesAnUnencodableTagCount(t *testing.T) {
	cmd, err := EncodeTSWrite("m", 0, 0, oversizedTags())
	if err == nil {
		t.Fatal("a tag map larger than the count field succeeded; the decoder " +
			"would read fewer tags than were written")
	}
	if cmd != nil {
		t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
	}
	var tooLong *FieldTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
	}
	if tooLong.Field != "tag count" {
		t.Fatalf("Field = %q, want \"tag count\"", tooLong.Field)
	}
}

// The boundary encodes. A guard that rejected the largest DESCRIBABLE value
// would be refusing valid input, which is its own defect.
func TestEncoders_AcceptTheLargestEncodableValues(t *testing.T) {
	key := bytes.Repeat([]byte("k"), MaxUint16Field)

	if _, err := EncodeMVCCPut(key, []byte("v"), 0); err != nil {
		t.Fatalf("the largest encodable key was refused by EncodeMVCCPut: %v", err)
	}
	if _, err := EncodeMVCCDelete(key); err != nil {
		t.Fatalf("the largest encodable key was refused by EncodeMVCCDelete: %v", err)
	}
	cmd, err := EncodeCompareAndDelete(key, []byte("v"))
	if err != nil {
		t.Fatalf("the largest encodable key was refused by EncodeCompareAndDelete: %v", err)
	}
	gotKey, gotExpected, derr := DecodeCompareAndDelete(cmd[1:])
	if derr != nil {
		t.Fatalf("DecodeCompareAndDelete: %v", derr)
	}
	if len(gotKey) != len(key) {
		t.Fatalf("round-tripped key length = %d, want %d", len(gotKey), len(key))
	}
	if string(gotExpected) != "v" {
		t.Fatalf("round-tripped expected value = %q, want %q", gotExpected, "v")
	}
}

// The four-byte fields are guarded too. A 4 GiB key is not reachable today,
// which is exactly what was said about the two-byte fields until a
// 65,537-byte key turned out to be one line of test code — so the guard is
// asserted at the helper, where it can be exercised without allocating 4 GiB.
//
// The bound is converted through a VARIABLE rather than written as a constant
// expression: int(MaxUint32Field) is a compile-time overflow where int is 32
// bits, and a test that does not build is not a test. Where int is 32 bits the
// case is also unreachable — len() cannot exceed MaxInt32, so nothing can carry
// a length past a uint32 bound — and the test says so instead of pretending to
// cover it.
func TestPutUint32Len_RefusesWhatItCannotDescribe(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("int is 32 bits here, so len() cannot reach the uint32 bound and " +
			"the guard is unreachable by construction")
	}

	buf := make([]byte, 4)
	bound := MaxUint32Field
	atBound := int(bound)

	if err := putUint32Len(buf, atBound, "item"); err != nil {
		t.Fatalf("the largest describable value was refused: %v", err)
	}

	err := putUint32Len(buf, atBound+1, "item")
	var tooLong *FieldTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
	}
	if tooLong.Field != "item" || tooLong.Max != MaxUint32Field {
		t.Fatalf("error = %+v, want Field=item Max=%d", tooLong, MaxUint32Field)
	}
}

// A negative length cannot come from len(), but checkUint32Len takes an int and
// the uint64 comparison it makes would read a negative as enormous. Refusing it
// keeps the conversion honest rather than relying on every caller upstream.
func TestCheckUint32Len_RefusesANegativeLength(t *testing.T) {
	var tooLong *FieldTooLongError
	if !errors.As(CheckUint32Len(-1, "item"), &tooLong) {
		t.Fatal("a negative length was accepted; converted to uint64 it is enormous")
	}
}

// The error says which field overflowed, by how much, and against what bound
// — enough to act on without decoding the frame.
func TestFieldTooLongError_Message(t *testing.T) {
	err := &FieldTooLongError{Field: "key", Length: 70000, Max: MaxUint16Field}
	msg := err.Error()
	for _, want := range []string{"key", "70000", "65535"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not mention %q", msg, want)
		}
	}
}

// The vector encoders live in their own file and frame namespaces, ids,
// metadata and vector payloads exactly as the KV encoders frame keys.
//
// They are here because I missed them: the audit that opened #52 grepped
// protocol.go rather than the package, so the first sweep left twelve
// unguarded casts in vector.go — the same omission the issue exists to fix,
// committed while fixing it. The guard is per-file only if the sweep is, so
// the coverage is per-file too.
func TestVectorEncoders_RefuseUnencodableFields(t *testing.T) {
	long := strings.Repeat("n", MaxUint16Field+2)

	for _, tc := range []struct {
		name  string
		field string
		call  func() ([]byte, error)
	}{
		{"EncodeVectorUpsert/namespace", "namespace", func() ([]byte, error) {
			return EncodeVectorUpsert(long, "id", []float32{1}, nil)
		}},
		{"EncodeVectorUpsert/id", "vector id", func() ([]byte, error) {
			return EncodeVectorUpsert("ns", long, []float32{1}, nil)
		}},
		{"EncodeVectorDelete/namespace", "namespace", func() ([]byte, error) {
			return EncodeVectorDelete(long, "id")
		}},
		{"EncodeVectorDelete/id", "vector id", func() ([]byte, error) {
			return EncodeVectorDelete("ns", long)
		}},
		{"EncodeNamespaceCreate", "namespace", func() ([]byte, error) {
			return EncodeNamespaceCreate(long, 8, 0, 0)
		}},
		{"EncodeNamespaceDelete", "namespace", func() ([]byte, error) {
			return EncodeNamespaceDelete(long)
		}},
		{"EncodeVectorBatchUpsert", "namespace", func() ([]byte, error) {
			return EncodeVectorBatchUpsert(long, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.call()
			if err == nil {
				t.Fatalf("encoding a %d-byte %s succeeded; it would decode as a "+
					"%d-byte value and address the wrong record",
					len(long), tc.field, len(long)%(MaxUint16Field+1))
			}
			if cmd != nil {
				t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
			}
			var tooLong *FieldTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
			}
			if tooLong.Field != tc.field {
				t.Fatalf("Field = %q, want %q", tooLong.Field, tc.field)
			}
		})
	}
}

// EncodeAppError is deliberately NOT guarded and deliberately still returns a
// single value: it has no length field at all — a fixed prefix followed by the
// message, decoded by stripping the prefix and taking the remainder. There is
// nothing that can overflow, so adding an error return would be uniformity for
// its own sake. Pinned so the exception is a decision on the record rather
// than an oversight someone later "fixes".
func TestEncodeAppError_HasNoLengthFieldToGuard(t *testing.T) {
	msg := strings.Repeat("m", MaxUint16Field+2)
	out := EncodeAppError(msg)

	got, ok := DecodeAppError(out)
	if !ok {
		t.Fatal("DecodeAppError did not recognise its own encoding")
	}
	if got != msg {
		t.Fatalf("round-tripped message length = %d, want %d — a message far past "+
			"any two-byte bound must survive intact, because no field frames it",
			len(got), len(msg))
	}
}

// Two length fields escaped the first sweep because they are bound to a
// variable before being written — `pointSize` in EncodeTSWriteBatch and `dim`
// in EncodeVectorUpsert. The sweep matched the SHAPE `uint16(len(x))` rather
// than the semantics "a length field", so a value computed one line earlier
// slipped through. These pin them by behaviour, which is shape-independent.
func TestEncodeTSWriteBatch_RefusesAnUnencodablePointLength(t *testing.T) {
	// One point whose encoded size exceeds the per-point length field: the
	// metric alone is under the metric field's own bound, so this can only be
	// refused by the point-length guard.
	tags := map[string]string{}
	for i := 0; i < 700; i++ {
		tags["tag"+strings.Repeat("x", 90)+string(rune('a'+i%26))+string(rune('a'+i/26))] = strings.Repeat("v", 90)
	}
	points := []TSWriteParams{{Metric: "m", Timestamp: 0, Value: 0, Tags: tags}}

	cmd, err := EncodeTSWriteBatch(points)
	if err == nil {
		t.Fatal("a point larger than its own length field encoded; the decoder " +
			"would read a shorter point and treat the remainder as the next one")
	}
	if cmd != nil {
		t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
	}
	var tooLong *FieldTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
	}
	if tooLong.Field != "point length" {
		t.Fatalf("Field = %q, want \"point length\"", tooLong.Field)
	}
}

// A tag key and a tag value are framed individually, inside the loop, and are
// guarded there — the site an up-front check cannot reach because the
// variables are loop-scoped.
func TestEncodeTSWrite_RefusesAnUnencodableTagKeyOrValue(t *testing.T) {
	long := strings.Repeat("t", MaxUint16Field+2)

	for _, tc := range []struct {
		name  string
		field string
		tags  map[string]string
	}{
		{"tag key", "tag key", map[string]string{long: "v"}},
		{"tag value", "tag value", map[string]string{"k": long}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := EncodeTSWrite("m", 0, 0, tc.tags)
			if err == nil {
				t.Fatalf("a %d-byte %s encoded", len(long), tc.field)
			}
			if cmd != nil {
				t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
			}
			var tooLong *FieldTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
			}
			if tooLong.Field != tc.field {
				t.Fatalf("Field = %q, want %q", tooLong.Field, tc.field)
			}
		})
	}
}

// The batch encoders frame their nested fields inside a loop, and used to
// validate them there — after the buffer for the WHOLE batch had already been
// sized and allocated from those same unvalidated lengths. Rejection still
// happened, so the tests above passed; what did not happen was rejection
// before the allocation. These pin the nested guards by the field they name,
// one subtest per field, so a future rewrite cannot quietly drop one.
func TestBatchEncoders_RefuseNestedUnencodableFields(t *testing.T) {
	long := strings.Repeat("b", MaxUint16Field+2)

	for _, tc := range []struct {
		name  string
		field string
		call  func() ([]byte, error)
	}{
		{"TSWriteBatch/metric", "metric", func() ([]byte, error) {
			return EncodeTSWriteBatch([]TSWriteParams{{Metric: long}})
		}},
		{"TSWriteBatch/tag count", "tag count", func() ([]byte, error) {
			return EncodeTSWriteBatch([]TSWriteParams{{Metric: "m", Tags: oversizedTags()}})
		}},
		{"TSWriteBatch/tag key", "tag key", func() ([]byte, error) {
			return EncodeTSWriteBatch([]TSWriteParams{{Metric: "m", Tags: map[string]string{long: "v"}}})
		}},
		{"TSWriteBatch/tag value", "tag value", func() ([]byte, error) {
			return EncodeTSWriteBatch([]TSWriteParams{{Metric: "m", Tags: map[string]string{"k": long}}})
		}},
		{"TSDeleteSeries/tag key", "tag key", func() ([]byte, error) {
			return EncodeTSDeleteSeries("m", 0, 1, map[string]string{long: "v"})
		}},
		{"TSDeleteSeries/tag value", "tag value", func() ([]byte, error) {
			return EncodeTSDeleteSeries("m", 0, 1, map[string]string{"k": long})
		}},
		{"VectorBatchUpsert/vector id", "vector id", func() ([]byte, error) {
			return EncodeVectorBatchUpsert("ns", []VectorUpsertParams{{ID: long, Vector: []float32{1}}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.call()
			if err == nil {
				t.Fatalf("an unencodable %s nested in a batch encoded; the decoder "+
					"would read a truncated length and misattribute the remainder", tc.field)
			}
			if cmd != nil {
				t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
			}
			var tooLong *FieldTooLongError
			if !errors.As(err, &tooLong) {
				t.Fatalf("error = %T (%v), want *FieldTooLongError", err, err)
			}
			if tooLong.Field != tc.field {
				t.Fatalf("Field = %q, want %q", tooLong.Field, tc.field)
			}
		})
	}
}

// Refusing an unencodable batch must not first allocate the batch.
//
// This is the property the reordering exists for, and it is invisible to an
// error-value assertion: the old code returned exactly the same error, having
// allocated the entire encoded request to get there. So measure the allocation
// instead. A caller that can submit a batch can therefore not turn a request
// the encoder is about to refuse into memory pressure proportional to it.
//
// The fixture shares one tag map across every well-formed point, so the input
// costs kilobytes while the encoded form it describes costs megabytes — the
// gap between what is allocated and what is merely described is the whole
// point of the test.
func TestEncodeTSWriteBatch_RefusesBeforeAllocating(t *testing.T) {
	const (
		goodPoints = 60000
		tagValue   = 1024
	)

	shared := map[string]string{"host": strings.Repeat("h", tagValue)}
	points := make([]TSWriteParams, 0, goodPoints+1)
	for i := 0; i < goodPoints; i++ {
		points = append(points, TSWriteParams{Metric: "m", Tags: shared})
	}

	// Codex's case: two tags that are individually encodable and jointly are
	// not, so only the point-length guard can refuse this.
	half := strings.Repeat("x", 40000)
	points = append(points, TSWriteParams{Metric: "m", Tags: map[string]string{"a": half, "b": half}})

	// What the old ordering would have allocated before reaching the bad point.
	wouldHaveAllocated := goodPoints * (2 + 2 + 1 + 8 + 8 + 2 + 2 + len("host") + 2 + tagValue)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	cmd, err := EncodeTSWriteBatch(points)

	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	if err == nil {
		t.Fatal("a point larger than its own length field encoded")
	}
	if cmd != nil {
		t.Fatalf("a refused encode returned %d bytes; it must return nil", len(cmd))
	}
	var tooLong *FieldTooLongError
	if !errors.As(err, &tooLong) || tooLong.Field != "point length" {
		t.Fatalf("error = %v, want a *FieldTooLongError naming \"point length\"", err)
	}

	// The measuring pass allocates pointSizes, one int per point, and nothing
	// else — bounded by the point count, which is validated first, and not by
	// the offending field. Budget an order of magnitude over that and still an
	// order of magnitude under the buffer the old ordering allocated, so the
	// assertion cannot be met by an implementation that allocates the batch.
	budget := uint64(10 * len(points) * 8)
	if allocated > budget {
		t.Fatalf("refusing the batch allocated %d bytes, over the %d-byte budget; "+
			"the encoder is sizing the request (~%d bytes) before validating it",
			allocated, budget, wouldHaveAllocated)
	}
}

// Splitting measuring from writing introduces a hazard the single-pass code did
// not have: the two halves can disagree, and a size that is too small panics on
// a slice bound while a size that is too large leaves trailing zero bytes the
// decoder reads as another field. framedSlicesSize and encodeFramedSlices are
// that pair, so they are pinned against each other directly rather than through
// a round trip that could absorb slack.
func TestFramedSlicesSize_AgreesWithEncodeFramedSlices(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items [][]byte
	}{
		{"nil", nil},
		{"empty run", [][]byte{}},
		{"one empty item", [][]byte{{}}},
		{"one item", [][]byte{[]byte("op")}},
		{"several", [][]byte{[]byte("a"), []byte("bb"), []byte("ccc")}},
		{"an empty item among others", [][]byte{[]byte("a"), {}, []byte("ccc")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size, err := framedSlicesSize(tc.items)
			if err != nil {
				t.Fatalf("framedSlicesSize: %v", err)
			}

			// Exactly the measured size, so a writer that needs one more byte
			// panics here rather than being masked by a generous buffer.
			buf := make([]byte, size)
			written, err := encodeFramedSlices(buf, tc.items)
			if err != nil {
				t.Fatalf("encodeFramedSlices: %v", err)
			}
			if written != size {
				t.Fatalf("measured %d bytes and wrote %d; a short measure panics and a "+
					"long one leaves trailing zeros the decoder reads as a field",
					size, written)
			}
		})
	}
}

// A sum an int cannot describe is refused rather than wrapped into a small
// positive number that make would accept.
//
// The field bounds do not close this on their own for the four-byte-framed
// encoders: MaxUint32Field items of MaxUint32Field bytes multiply out past
// math.MaxInt64. No machine can hold such an input, which is exactly the
// "unreachable in practice" this package exists to stop relying on.
func TestAddSize_RefusesASumIntCannotDescribe(t *testing.T) {
	if got, err := addSize(math.MaxInt-2, 2); err != nil || got != math.MaxInt {
		t.Fatalf("addSize(MaxInt-2, 2) = %d, %v; want the largest describable sum", got, err)
	}

	got, err := addSize(math.MaxInt-1, 2)
	if err == nil {
		t.Fatalf("addSize(MaxInt-1, 2) = %d with no error; the sum wrapped negative "+
			"and make would have been asked for a nonsense length", got)
	}
	var overflow *EncodedSizeOverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error = %T (%v), want *EncodedSizeOverflowError", err, err)
	}
	if got != 0 {
		t.Fatalf("a refused sum returned %d; it must return 0", got)
	}
}

// The framed encoders size their buffer from framedSlicesSize, so the command
// they return must be exactly as long as the frame layout says — no slack for a
// decoder to misread. Computed here independently of the encoder.
func TestFramedEncoders_EmitExactlyTheFrameTheyDescribe(t *testing.T) {
	compares := [][]byte{[]byte("cmp-one"), []byte("cmp-two")}
	successOps := [][]byte{[]byte("s")}
	failureOps := [][]byte{[]byte("fail-op"), []byte("")}

	run := func(items [][]byte) int {
		n := frameSize
		for _, item := range items {
			n += frameSize + len(item)
		}
		return n
	}

	txn, err := EncodeTransaction(compares, successOps, failureOps)
	if err != nil {
		t.Fatalf("EncodeTransaction: %v", err)
	}
	wantTxn := headerSize + run(compares) + run(successOps) + run(failureOps)
	if len(txn) != wantTxn {
		t.Fatalf("EncodeTransaction produced %d bytes, want %d", len(txn), wantTxn)
	}

	batch, err := EncodeBatch(successOps)
	if err != nil {
		t.Fatalf("EncodeBatch: %v", err)
	}
	if wantBatch := headerSize + run(successOps); len(batch) != wantBatch {
		t.Fatalf("EncodeBatch produced %d bytes, want %d", len(batch), wantBatch)
	}
}

// The bound is checked BEFORE the buffer is sized, so an unencodable input
// costs no allocation proportional to itself. checkUint32Len is the predicate
// both the pre-allocation check and the write-time guard share, so the bound
// has exactly one definition.
func TestCheckAndPutShareOneBound(t *testing.T) {
	buf := make([]byte, 4)

	if checkUint16Len(MaxUint16Field, "key") != nil {
		t.Fatal("the largest describable uint16 length was refused by the check")
	}
	if putUint16Len(buf, MaxUint16Field, "key") != nil {
		t.Fatal("the largest describable uint16 length was refused by the writer")
	}

	var a, b *FieldTooLongError
	if !errors.As(checkUint16Len(MaxUint16Field+1, "key"), &a) {
		t.Fatal("check accepted a length one past the bound")
	}
	if !errors.As(putUint16Len(buf, MaxUint16Field+1, "key"), &b) {
		t.Fatal("writer accepted a length one past the bound")
	}
	if *a != *b {
		t.Fatalf("check reported %+v and writer reported %+v; the bound has two "+
			"definitions and they can drift", a, b)
	}
}

// A checked SUM is not enough on its own where int is 32 bits: a vector's byte
// length is its element count times four, and an element count that passes
// checkUint32Len still multiplies past the maximum there.
func TestMulSize_RefusesAProductIntCannotDescribe(t *testing.T) {
	if got, err := mulSize(3, 4); err != nil || got != 12 {
		t.Fatalf("mulSize(3, 4) = %d, %v; want 12", got, err)
	}

	limit := math.MaxInt / 4
	if got, err := mulSize(limit, 4); err != nil || got != limit*4 {
		t.Fatalf("mulSize at the boundary = %d, %v; want the largest describable product", got, err)
	}

	got, err := mulSize(limit+1, 4)
	if err == nil {
		t.Fatalf("mulSize(MaxInt/4+1, 4) = %d with no error; the product wrapped and "+
			"make would have been asked for a nonsense length", got)
	}
	var overflow *EncodedSizeOverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error = %T (%v), want *EncodedSizeOverflowError", err, err)
	}
	if got != 0 {
		t.Fatalf("a refused product returned %d; it must return 0", got)
	}

	if _, err := mulSize(-1, 4); err == nil {
		t.Fatal("a negative count was accepted; multiplied out it is meaningless")
	}
}

// addSize must refuse a NEGATIVE operand, not only a sum past the maximum.
//
// A call site that pre-sums its components — `frameSize + len(item)` — hands
// over an already-wrapped term where int is 32 bits and len is near its
// maximum. An upper-bound check alone accepts it and SHRINKS the total, so the
// encoder allocates a buffer smaller than the frame it then writes. That is a
// silent undersized allocation, which is worse than the overflow it came from.
func TestAddSize_RefusesANegativeOperand(t *testing.T) {
	for _, tc := range []struct {
		name       string
		total, add int
	}{
		{"negative addend", 16, -1},
		{"negative total", -1, 16},
		{"both negative", -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := addSize(tc.total, tc.add)
			if err == nil {
				t.Fatalf("addSize(%d, %d) = %d with no error; a negative term shrinks "+
					"the running total and the buffer is then undersized",
					tc.total, tc.add, got)
			}
			var overflow *EncodedSizeOverflowError
			if !errors.As(err, &overflow) {
				t.Fatalf("error = %T (%v), want *EncodedSizeOverflowError", err, err)
			}
		})
	}
}

// addSizes checks EVERY addition, which is the point of passing components
// separately rather than pre-summing them at the call site.
func TestAddSizes_ChecksEachComponent(t *testing.T) {
	got, err := addSizes(1, 2, 3, 4)
	if err != nil || got != 10 {
		t.Fatalf("addSizes(1, 2, 3, 4) = %d, %v; want 10", got, err)
	}

	// A bad component anywhere in the run is caught, not just the first.
	if _, err := addSizes(1, 2, -3, 4); err == nil {
		t.Fatal("addSizes accepted a negative component in the middle of the run")
	}
	if _, err := addSizes(math.MaxInt-1, 1, 1); err == nil {
		t.Fatal("addSizes accepted a run whose LATER addition overflows")
	}
}

// EncodeOp and EncodeCompare accumulate their size through addSize rather than
// summing first, because nothing stops a caller passing the SAME slice as key,
// value and rangeEnd. Each length is individually legal; the SUM is what has to
// be checked. The aliasing is the point, so the test does it.
func TestEncodeOp_AcceptsAliasedFieldsWithinTheBound(t *testing.T) {
	shared := bytes.Repeat([]byte("x"), 1024)

	cmd, err := EncodeOp(OpMVCCPut, shared, shared, shared, 0)
	if err != nil {
		t.Fatalf("EncodeOp with one slice aliased three ways: %v", err)
	}
	want := 1 + 8 + 3*(frameSize+len(shared))
	if len(cmd) != want {
		t.Fatalf("encoded %d bytes, want %d — the size must count each framed field "+
			"once, aliased or not", len(cmd), want)
	}

	cmp, err := EncodeCompare(shared, 0, 0, shared, 0, 0, 0)
	if err != nil {
		t.Fatalf("EncodeCompare with key and value aliased: %v", err)
	}
	if wantCmp := 1 + 1 + 8 + 8 + 8 + 2*(frameSize+len(shared)); len(cmp) != wantCmp {
		t.Fatalf("encoded %d bytes, want %d", len(cmp), wantCmp)
	}
}

// A hostile element count must be REFUSED, not decoded as an empty command.
//
// `int(uint32)` is lossless only where int is 64 bits. Where it is 32, a count
// past MaxInt32 converts negative, every `for i := 0; i < count` loop is
// skipped, and a frame of `ff ff ff ff` decodes as an empty batch or
// transaction with a NIL error. Fail-open deserialization on the apply path,
// and — worse for a replicated state machine — the same bytes decoding
// differently on different architectures.
//
// The payload bound catches it on 64-bit too, where the conversion is harmless
// and the count is still impossible, so this is a normal test rather than an
// architecture-gated one.
//
// It asserts the SPECIFIC rejection through countRefusedUpFront, not merely
// that an error came back. An
// unchecked decoder also errors on 64-bit — it walks the impossible count and
// trips "truncated op length" on the first missing element — so "some error"
// passes with the defect intact and proves nothing. Naming the count check
// pins that the frame is refused UP FRONT, which is what makes the behaviour
// the same on every architecture.
// countRefusedUpFront reports whether reason names one of the two ways
// decodeCount refuses an impossible element count. Which one fires depends on
// the width of int: where it is 64 bits every uint32 is representable and the
// count fails the payload bound, and where it is 32 bits a count above MaxInt32
// is refused as unrepresentable before the bound is reached. Both refuse the
// frame up front, which is the property under test; pinning either string alone
// fails under GOARCH=386 for correct behaviour.
func countRefusedUpFront(reason string) bool {
	return strings.Contains(reason, "count exceeds payload") ||
		strings.Contains(reason, "count is not representable")
}

func TestDecoders_RefuseAnImpossibleElementCount(t *testing.T) {
	frame := func(n uint32) []byte {
		b := make([]byte, frameSize)
		binary.LittleEndian.PutUint32(b, n)
		return b
	}

	for _, count := range []uint32{0xffffffff, 0x80000000, 0x7fffffff, 16} {
		t.Run("batch/"+strconv.FormatUint(uint64(count), 16), func(t *testing.T) {
			// A count with NO elements following it: impossible at any width.
			ops, err := DecodeBatch(frame(count))
			if err == nil {
				t.Fatalf("DecodeBatch accepted a count of %#x with an empty payload and "+
					"returned %d ops; a malformed frame must be refused, not decoded "+
					"as an empty command", count, len(ops))
			}
			var invalid *InvalidCommandError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T (%v), want *InvalidCommandError", err, err)
			}
			if !countRefusedUpFront(invalid.Reason) {
				t.Fatalf("reason = %q, want the COUNT to be rejected up front; "+
					"walking the count and tripping on a missing element is what an "+
					"unchecked decoder does, and it converts negative on 32-bit",
					invalid.Reason)
			}
		})

		t.Run("txn/"+strconv.FormatUint(uint64(count), 16), func(t *testing.T) {
			_, err := DecodeTransaction(frame(count))
			if err == nil {
				t.Fatalf("DecodeTransaction accepted a compare count of %#x with an "+
					"empty payload", count)
			}
			var invalid *InvalidCommandError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T (%v), want *InvalidCommandError", err, err)
			}
			if !countRefusedUpFront(invalid.Reason) {
				t.Fatalf("reason = %q, want the COUNT to be rejected up front", invalid.Reason)
			}
		})
	}
}

// And the counts that ARE possible still decode, so the bound is a distinction
// rather than a blanket refusal.
func TestDecoders_StillAcceptAWellFormedCount(t *testing.T) {
	op, err := EncodeOp(OpMVCCPut, []byte("k"), []byte("v"), nil, 0)
	if err != nil {
		t.Fatalf("EncodeOp: %v", err)
	}
	cmd, err := EncodeBatch([][]byte{op, op})
	if err != nil {
		t.Fatalf("EncodeBatch: %v", err)
	}
	ops, err := DecodeBatch(cmd[headerSize:])
	if err != nil {
		t.Fatalf("DecodeBatch on a well-formed batch: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("decoded %d ops, want 2", len(ops))
	}

	txn, err := EncodeTransaction(nil, [][]byte{op}, nil)
	if err != nil {
		t.Fatalf("EncodeTransaction: %v", err)
	}
	decoded, err := DecodeTransaction(txn[headerSize:])
	if err != nil {
		t.Fatalf("DecodeTransaction on a well-formed transaction: %v", err)
	}
	if len(decoded.SuccessOps) != 1 {
		t.Fatalf("decoded %d success ops, want 1", len(decoded.SuccessOps))
	}
}

// An element length field that the remaining payload cannot hold must be
// refused before it reaches slice arithmetic.
//
// The failure mode is architecture-specific and that is the whole point. Where
// int is 64 bits, `off+int(raw)` is wide enough that the old bounds check
// caught this. Where int is 32 bits it is not: a length of 0x7fffffff passes
// the `< 0` guard, `off+n` wraps NEGATIVE, `off+n > len(payload)` is false, and
// the decoder slices `payload[off:negative]` — a panic on input any client can
// send, in a decoder that runs on every replica before the command is applied.
//
// So this test is nearly vacuous on amd64 and load-bearing on 386; the gate
// runs `GOARCH=386 go test ./pkg/protocol/` for exactly that reason. Each case
// is the smallest payload that reaches the length field of one decoder
// (go-qrdb#52).
func TestDecoders_RefuseAnElementLengthTheremainingPayloadCannotHold(t *testing.T) {
	// 0x7fffffff is MaxInt32: the largest value that survives a conversion to
	// a 32-bit int as a positive number, and therefore the one that overflows
	// the addition instead of being caught by the sign check.
	const overflowing = uint32(0x7fffffff)

	u32 := func(vs ...uint32) []byte {
		b := make([]byte, 0, len(vs)*4)
		for _, v := range vs {
			b = binary.LittleEndian.AppendUint32(b, v)
		}
		return b
	}

	tests := []struct {
		name    string
		decode  func([]byte) error
		payload []byte
		want    string
	}{
		{
			name:    "batch op length",
			decode:  func(p []byte) error { _, err := DecodeBatch(p); return err },
			payload: u32(1, overflowing),
			want:    "batch: op exceeds payload",
		},
		{
			name:    "transaction compare length",
			decode:  func(p []byte) error { _, err := DecodeTransaction(p); return err },
			payload: u32(1, overflowing),
			want:    "txn: compare exceeds payload",
		},
		{
			// No compares, then one success op whose length overflows.
			name:    "transaction success op length",
			decode:  func(p []byte) error { _, err := DecodeTransaction(p); return err },
			payload: u32(0, 1, overflowing),
			want:    "txn: success op exceeds payload",
		},
		{
			name:    "op key length",
			decode:  func(p []byte) error { _, _, err := DecodeOp(p); return err },
			payload: append([]byte{OpFSMPut}, u32(overflowing)...),
			want:    "op: key exceeds data",
		},
		{
			// Empty key, then a value length that overflows.
			name:    "op value length",
			decode:  func(p []byte) error { _, _, err := DecodeOp(p); return err },
			payload: append([]byte{OpFSMPut}, u32(0, overflowing)...),
			want:    "op: value exceeds data",
		},
		{
			// Empty key and value, then a range end length that overflows.
			name:    "op range end length",
			decode:  func(p []byte) error { _, _, err := DecodeOp(p); return err },
			payload: append([]byte{OpFSMPut}, u32(0, 0, overflowing)...),
			want:    "op: range end exceeds data",
		},
		{
			name:    "compare key length",
			decode:  func(p []byte) error { _, _, err := DecodeCompare(p); return err },
			payload: u32(overflowing),
			want:    "compare: key exceeds data",
		},
		{
			// Empty key, target and result bytes, then an overflowing value
			// length.
			name:    "compare value length",
			decode:  func(p []byte) error { _, _, err := DecodeCompare(p); return err },
			payload: append(append(u32(0), 0, 0), u32(overflowing)...),
			want:    "compare: value exceeds data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A panic here IS the bug, so name it rather than letting the
			// suite report an anonymous crash in an unrelated test.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decoder panicked on an overflowing length: %v", r)
				}
			}()

			err := tt.decode(tt.payload)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			var invalid *InvalidCommandError
			if !errors.As(err, &invalid) {
				t.Fatalf("error is %T, want *InvalidCommandError", err)
			}
			if invalid.Reason != tt.want {
				t.Errorf("reason = %q, want %q", invalid.Reason, tt.want)
			}
		})
	}
}

// decodeLen must never return a length the caller can then slice past the end
// with, whatever the raw field says. The uint64 comparison is what makes the
// bound exact; an implementation that converts first and compares as int is
// correct on 64-bit and wrong on 32.
func TestDecodeLen(t *testing.T) {
	tests := []struct {
		name      string
		raw       uint32
		remaining int
		wantN     int
		wantErr   bool
	}{
		{name: "zero length always fits", raw: 0, remaining: 0, wantN: 0},
		{name: "exactly the remaining bytes", raw: 4, remaining: 4, wantN: 4},
		{name: "one byte past the remaining bytes", raw: 5, remaining: 4, wantErr: true},
		{name: "max int32 with nothing remaining", raw: 0x7fffffff, remaining: 0, wantErr: true},
		{name: "max uint32 with nothing remaining", raw: math.MaxUint32, remaining: 0, wantErr: true},
		{name: "negative remaining is never satisfiable", raw: 0, remaining: -1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := decodeLen(tt.raw, tt.remaining, "test: length exceeds payload")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("decodeLen(%d, %d) = %d, want an error", tt.raw, tt.remaining, n)
				}
				var invalid *InvalidCommandError
				if !errors.As(err, &invalid) {
					t.Fatalf("error is %T, want *InvalidCommandError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeLen(%d, %d): %v", tt.raw, tt.remaining, err)
			}
			if n != tt.wantN {
				t.Errorf("n = %d, want %d", n, tt.wantN)
			}
			if n > tt.remaining {
				t.Errorf("n = %d exceeds remaining = %d", n, tt.remaining)
			}
		})
	}
}

// The fixed fields of a time-series frame must be combined with the tag block
// through checked arithmetic, not with a raw `+`.
//
// tagBlockSize proves that the tag block alone fits an int. That is a weaker
// claim than the allocation needs: the buffer is the tag block PLUS the opcode,
// the metric and four fixed integers, and it was the sum that reached make().
// Testing the helper directly is what makes this checkable on every
// architecture — tagsSize arrives as a parameter, so MaxInt costs nothing to
// pass, where reaching the same state through a real map would cost a 32-bit
// address space (go-qrdb#52).
func TestTSMetricFrameSize(t *testing.T) {
	// [op:1][metricLen:2][int64:8][int64:8][tagCount:2] — everything the frame
	// carries besides the metric bytes and the tag block.
	const fixed = headerSize + 2 + 8 + 8 + 2

	tests := []struct {
		name      string
		metricLen int
		tagsSize  int
		want      int
		wantErr   bool
	}{
		{name: "empty frame", metricLen: 0, tagsSize: 0, want: fixed},
		{name: "ordinary frame", metricLen: 5, tagsSize: 10, want: fixed + 15},
		{
			name:      "largest representable sum",
			metricLen: 0,
			tagsSize:  math.MaxInt - fixed,
			want:      math.MaxInt,
		},
		{
			name:      "one byte past the largest representable sum",
			metricLen: 0,
			tagsSize:  math.MaxInt - fixed + 1,
			wantErr:   true,
		},
		{
			// The tag block fits on its own; the metric is what tips it. This
			// is the case a check on tagsSize alone cannot catch.
			name:      "the metric tips an otherwise valid tag block",
			metricLen: 1,
			tagsSize:  math.MaxInt - fixed,
			wantErr:   true,
		},
		{name: "maximum tag block", metricLen: 0, tagsSize: math.MaxInt, wantErr: true},
		{name: "negative tag block", metricLen: 0, tagsSize: -1, wantErr: true},
		{name: "negative metric length", metricLen: -1, tagsSize: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size, err := tsMetricFrameSize(tt.metricLen, tt.tagsSize)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("tsMetricFrameSize(%d, %d) = %d, want an error",
						tt.metricLen, tt.tagsSize, size)
				}
				var overflow *EncodedSizeOverflowError
				if !errors.As(err, &overflow) {
					t.Fatalf("error is %T, want *EncodedSizeOverflowError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("tsMetricFrameSize(%d, %d): %v", tt.metricLen, tt.tagsSize, err)
			}
			if size != tt.want {
				t.Errorf("size = %d, want %d", size, tt.want)
			}
		})
	}
}

// And the overflow is reachable through the public encoders with a tag map that
// costs a few megabytes, which is what makes it a defect rather than a
// theoretical one.
//
// The trick is that a tag block's LOGICAL encoded size is unrelated to its
// physical size: every entry can share one value string, so 32,000 entries
// naming the same 64 KiB value encode as two gigabytes while occupying a few
// megabytes of heap. Where int is 32 bits that sum plus the fixed fields wraps
// negative and make() panics. Where int is 64 bits the same state would need a
// real exabyte, so this can only run on a 32-bit build — the gate runs
// GOARCH=386 for exactly this class (go-qrdb#52).
func TestTSEncoders_RefuseATagBlockThatOverflowsWithTheFixedFields(t *testing.T) {
	if strconv.IntSize != 32 {
		t.Skipf("int is %d bits; a tag block this large is not constructible here",
			strconv.IntSize)
	}

	// One value string, shared by every entry: this is the whole point.
	value := strings.Repeat("v", MaxUint16Field)

	// Add uniform entries until one more would exceed MaxInt. Because the
	// per-entry contribution is smaller than the window in which the metric can
	// tip the sum, the largest such total always lands inside it.
	tags := make(map[string]string)
	tagsSize := 0
	for i := 0; ; i++ {
		key := strconv.Itoa(i)
		per := 2 + len(key) + 2 + len(value)
		if tagsSize > math.MaxInt-per {
			break
		}
		tags[key] = value
		tagsSize += per
		if len(tags) == MaxUint16Field {
			break
		}
	}

	const fixed = headerSize + 2 + 8 + 8 + 2
	metricLen := 0
	if tagsSize <= math.MaxInt-fixed {
		// Smallest metric that pushes the total past MaxInt.
		metricLen = math.MaxInt - fixed - tagsSize + 1
	}
	if metricLen > MaxUint16Field {
		t.Fatalf("metric of %d bytes needed to tip the sum, but the field caps it at %d",
			metricLen, MaxUint16Field)
	}
	metric := strings.Repeat("m", metricLen)

	// The tag block on its own is representable — that is what makes the
	// unchecked sum wrong rather than the tag block being oversized.
	measured, err := tagBlockSize(tags)
	if err != nil {
		t.Fatalf("tagBlockSize refused the block itself: %v; this test needs a block that PASSES", err)
	}
	if measured != tagsSize {
		t.Fatalf("tagBlockSize = %d, want %d", measured, tagsSize)
	}

	for _, tt := range []struct {
		name   string
		encode func() ([]byte, error)
	}{
		{"EncodeTSWrite", func() ([]byte, error) {
			return EncodeTSWrite(metric, 1, 2, tags)
		}},
		{"EncodeTSDeleteSeries", func() ([]byte, error) {
			return EncodeTSDeleteSeries(metric, 1, 2, tags)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("encoder panicked instead of refusing the size: %v", r)
				}
			}()

			buf, err := tt.encode()
			if err == nil {
				t.Fatalf("encoded %d bytes, want the size refused", len(buf))
			}
			var overflow *EncodedSizeOverflowError
			if !errors.As(err, &overflow) {
				t.Fatalf("error is %T (%v), want *EncodedSizeOverflowError", err, err)
			}
		})
	}
}
