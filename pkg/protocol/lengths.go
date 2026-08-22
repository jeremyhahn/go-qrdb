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
	"encoding/binary"
	"math"
	"strconv"
)

// Length-field bounds. A field describes at most what its width can express;
// anything longer is not encodable and must be refused rather than wrapped.
const (
	// MaxUint16Field is the largest value a two-byte length field can hold.
	MaxUint16Field = math.MaxUint16

	// MaxUint32Field is the largest value a four-byte length field can hold.
	//
	// Typed uint64, not left untyped. An untyped 4,294,967,295 adopts the type
	// of whatever it meets, and where that is an int on a 32-bit platform it
	// does not fit: comparing it against an int, or storing it in an int field,
	// is a COMPILE error there, not a runtime one. The whole package stopped
	// building under GOARCH=386 for exactly that reason.
	MaxUint32Field uint64 = math.MaxUint32
)

// FieldTooLongError is returned when a value cannot be described by the
// length field that frames it.
//
// The truncation this prevents is silent and it moves data between fields. A
// 65,537-byte key written into a two-byte field stores a length of 1, so the
// DECODER reads a one-byte key and folds the remaining 65,536 bytes into
// whatever field comes next. On a write that stores under the wrong key; on a
// delete it removes a different, shorter record than the caller named. Inside
// a replicated state machine, from input a caller can craft.
//
// Field names the length field, so an error identifies which one overflowed
// without the caller decoding the frame.
type FieldTooLongError struct {
	// Field is the length field that could not describe the value.
	Field string
	// Length is the offending length, in bytes or elements. An int, because
	// every value it holds came from len().
	Length int
	// Max is the largest value the field can describe. A uint64, because
	// MaxUint32Field does not fit an int on a 32-bit platform.
	Max uint64
}

func (e *FieldTooLongError) Error() string {
	return "protocol: " + e.Field + " length " + strconv.Itoa(e.Length) +
		" exceeds the " + strconv.FormatUint(e.Max, 10) + " its length field can describe"
}

// checkUint16Len reports whether n fits a two-byte length field.
//
// It exists so an encoder can refuse BEFORE it allocates. The write-time
// guard in putUint16Len is what makes omission unrepresentable, but by the
// time it runs the buffer is already sized from the very length being
// refused — so an unencodable input costs an allocation proportional to
// itself before being rejected. Encoders validate up front and write through
// the guards anyway: the check is cheap, and defence in depth here costs one
// comparison.
func checkUint16Len(n int, field string) error {
	if n > MaxUint16Field {
		return &FieldTooLongError{Field: field, Length: n, Max: MaxUint16Field}
	}
	return nil
}

// CheckUint32Len reports whether n fits a four-byte length field. See
// checkUint16Len for why the check is separate from the write.
// The comparison is made in uint64 space because the bound does not fit an int
// everywhere. On a platform where it does not, len() cannot reach it either, so
// the guard correctly never fires there rather than failing to build.
//
// It is exported because the bound has a second caller outside this package:
// pkg/store frames snapshot keys and values in the same four-byte field
// (go-qrdb#54). Exporting the predicate rather than letting that package grow
// its own copy is the point — two definitions of one bound is exactly the drift
// this whole family of defects comes from, and TestCheckAndPutShareOneBound
// pins that the check and the write agree.
func CheckUint32Len(n int, field string) error {
	if n < 0 || uint64(n) > MaxUint32Field {
		return &FieldTooLongError{Field: field, Length: n, Max: MaxUint32Field}
	}
	return nil
}

// EncodedSizeOverflowError is returned when an encoder's running size total
// cannot be described by an int, so the buffer it would size is not a buffer
// that can exist.
//
// The field bounds do not rule this out on their own everywhere. A run of
// four-byte-framed items admits up to MaxUint32Field items of up to
// MaxUint32Field bytes, whose product is larger than math.MaxInt64 — so the
// checked lengths prove the total fits only for the two-byte-framed encoders.
// Every physical limit says such an input cannot be constructed, but this
// package's whole subject is refusing to take a length on trust, and a bound
// that is checked cannot regress into one that is merely improbable.
type EncodedSizeOverflowError struct {
	// Total is the running size, in bytes, before the term that overflowed.
	Total int
	// Add is the term that could not be added.
	Add int
}

func (e *EncodedSizeOverflowError) Error() string {
	return "protocol: encoded size " + strconv.Itoa(e.Total) + " plus " +
		strconv.Itoa(e.Add) + " overflows int"
}

// addSize returns total+add, refusing a sum an int cannot describe.
//
// A NEGATIVE operand is refused too, and that guard is not paranoia about
// callers: a composite term built before the call — `frameSize + len(item)` —
// wraps to a negative where int is 32 bits and len is near its maximum. An
// upper-bound check alone accepts such a term and quietly SHRINKS the running
// total, so the encoder allocates a buffer smaller than the frame it is about
// to write. Refusing negatives turns that into a typed error at the one place
// every accumulation passes through, whatever shape the term arrived in.
func addSize(total, add int) (int, error) {
	if total < 0 || add < 0 || add > math.MaxInt-total {
		return 0, &EncodedSizeOverflowError{Total: total, Add: add}
	}
	return total + add, nil
}

// addSizes folds several components into total, checking EVERY addition.
//
// It exists so a call site cannot pre-sum its components into one composite
// term and hand the already-wrapped result to addSize. Passing the components
// separately is what makes the check cover each one.
func addSizes(total int, components ...int) (int, error) {
	var err error
	for _, c := range components {
		if total, err = addSize(total, c); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// mulSize returns n*factor, refusing a product an int cannot describe.
//
// Needed because a checked SUM is not enough on its own: a vector's byte length
// is its element count times four, and where int is 32 bits an element count
// that passes checkUint32Len still multiplies past the maximum. factor is a
// small positive constant at every call site, so the guard is one division.
func mulSize(n, factor int) (int, error) {
	if n < 0 || factor <= 0 {
		return 0, &EncodedSizeOverflowError{Total: n, Add: factor}
	}
	if n > math.MaxInt/factor {
		return 0, &EncodedSizeOverflowError{Total: n, Add: factor}
	}
	return n * factor, nil
}

// framedSlicesSize validates a count-prefixed run of length-prefixed slices and
// returns the number of bytes its encoded form occupies, including the count
// prefix. It is the measuring half of encodeFramedSlices, which is the writing
// half; the two must agree on the frame layout.
func framedSlicesSize(items [][]byte) (int, error) {
	if err := CheckUint32Len(len(items), "item count"); err != nil {
		return 0, err
	}

	size := frameSize
	for _, item := range items {
		if err := CheckUint32Len(len(item), "item"); err != nil {
			return 0, err
		}
		var err error
		if size, err = addSizes(size, frameSize, len(item)); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// tagBlockSize validates a tag map and returns the number of bytes its encoded
// form occupies: a two-byte key length and a two-byte value length beside every
// key and value.
//
// Measuring and validating are one pass because separating them is what let the
// batch encoders size a buffer from lengths they had not checked yet. A caller
// that needs the size needs the guarantee that the size is encodable, so the
// only way to get one here is to get both.
//
// The tag count is checked too, since it is framed by the same block.
func tagBlockSize(tags map[string]string) (int, error) {
	if err := checkUint16Len(len(tags), "tag count"); err != nil {
		return 0, err
	}

	size := 0
	for k, v := range tags {
		if err := checkUint16Len(len(k), "tag key"); err != nil {
			return 0, err
		}
		if err := checkUint16Len(len(v), "tag value"); err != nil {
			return 0, err
		}
		var err error
		if size, err = addSizes(size, 2, len(k), 2, len(v)); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// tsMetricFrameSize returns the encoded size of a time-series frame carrying a
// metric name, two eight-byte fields and a tag block:
//
//	[op:1][metricLen:2][metric][int64:8][int64:8][tagCount:2][tags...]
//
// EncodeTSWrite and EncodeTSDeleteSeries share that layout exactly, and both
// combined the fixed fields with tagsSize using unchecked addition. tagBlockSize
// proves only that tagsSize ALONE fits an int, which is not the same claim: a
// tag map whose logical encoded size approaches MaxInt while its physical size
// stays small — many entries sharing one large value string, which costs one
// copy of the string and a pointer per entry — leaves a sum that wraps negative
// where int is 32 bits, and make() panics on a negative length. Combining
// through addSizes refuses the sum instead, and having one helper rather than
// two copies of the expression is what keeps the two encoders from drifting
// apart again (go-qrdb#52).
func tsMetricFrameSize(metricLen, tagsSize int) (int, error) {
	return addSizes(headerSize, 2, metricLen, 8, 8, 2, tagsSize)
}

// putUint16Len writes a two-byte length field, refusing a value the field
// cannot describe.
//
// It exists so the bound cannot be forgotten at a call site. Every encoder in
// this package used to write `uint16(len(x))` directly, and every one of them
// was silently truncating: the cast is not a compile error, produces no
// runtime signal, and its consequence surfaces only in the decoder, as
// misattributed bytes. One writer beside the contract makes the omission
// unrepresentable rather than a thing each site must remember (go-qrdb#52).
//
// dst is the destination slice, matching the shape of the binary.Put* call it
// replaces, so a conversion cannot silently move an offset.
func putUint16Len(dst []byte, n int, field string) error {
	if err := checkUint16Len(n, field); err != nil {
		return err
	}
	binary.LittleEndian.PutUint16(dst, uint16(n))
	return nil
}

// putUint32Len writes a four-byte length field, refusing a value the field
// cannot describe.
//
// The four-byte fields need this as much as the two-byte ones do, even though
// a 4 GiB key is not reachable today. "Unreachable in practice" is exactly
// what the two-byte fields were assumed to be until a 65,537-byte key turned
// out to be one line of test code; a bound that is checked cannot regress into
// one that is merely improbable.
func putUint32Len(dst []byte, n int, field string) error {
	if err := CheckUint32Len(n, field); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(dst, uint32(n))
	return nil
}
