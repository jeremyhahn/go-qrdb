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
	"encoding/binary"
	"math"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/index/internal/indexkey"
)

// Numeric index terms.
//
// A numeric index stores each value as a term whose byte order IS the
// numeric order of the value, so a byte-range scan over the terms is a
// numeric range scan. The term encodes the decimal text exactly -- it is not
// a binary float -- so two distinct numbers never share a term and no
// integer, however large, is rounded onto its neighbour.
//
// The total order, lowest first:
//
//	-Inf < negative numbers < 0 < positive numbers < +Inf
//
// -0 is 0: both texts produce the same term. NaN has no place in a total
// order and is not indexed -- a record whose field is NaN is skipped like a
// record whose field is not a number -- and a NaN scan bound is refused.
//
// Term layout, a tag byte then, for a finite non-zero value, a body:
//
//	0x01  -Inf
//	0x02  negative  body, every byte inverted
//	0x03  zero
//	0x04  positive  body
//	0x05  +Inf
//
// The body writes |v| as 0.D1D2...Dn x 10^E with D1 and Dn non-zero: E as a
// big-endian int32 with its sign bit flipped, then the digits two to a byte
// (1 + 11*Da + (Db+1), or 1 + 11*Da for a final odd digit, so every digit
// byte is in 1..110), then a 0x00 terminator. A larger exponent is a larger
// magnitude; at equal exponents the digit bytes compare as the digit
// strings do, and the terminator sorts below any digit byte, so a shorter
// digit string -- the smaller magnitude, because trailing zeros are gone --
// sorts first. Inverting the body of a negative term reverses that order.
//
// Every term is prefix-free: no term is a proper prefix of another. That is
// what lets an index key place a separator and the entity key after the term
// and still sort by value first.

// NumericEncodingVersion is the version of the numeric term encoding this
// package writes. An index definition records the version its stored terms
// were written with (IndexConfig.EncodingVersion); a numeric definition with
// any other version holds terms this package cannot order and must be
// rebuilt -- its entries cleared and backfilled -- before it is served.
//
// Version 0 is the unversioned encoding that preceded this one: a float64
// with its sign bit flipped. It lost every integer above 2^53 to rounding,
// sorted -0 below every negative number, and gave NaN an arbitrary position.
const NumericEncodingVersion = 1

const (
	numericTagNegInf byte = 0x01
	numericTagNeg    byte = 0x02
	numericTagZero   byte = 0x03
	numericTagPos    byte = 0x04
	numericTagPosInf byte = 0x05

	// numericExponentLen is the width of the body's exponent field.
	numericExponentLen = 4

	// numericTerminator ends a positive body; a negative body ends with its
	// inverse.
	numericTerminator byte = 0x00

	// numericDigitBase is the radix of the second digit in a digit byte:
	// ten digits plus "no second digit".
	numericDigitBase = 11

	// numericExponentSaturation caps the exponent text's magnitude while it
	// is read, so arbitrarily long exponent digits cannot overflow. Any
	// value this large is already outside int32 and is refused.
	numericExponentSaturation = int64(1) << 40
)

// decimal is a parsed number: a sign, and either an infinity or the
// normalized digits and exponent of 0.digits x 10^exp. Zero has no digits.
type decimal struct {
	neg    bool
	inf    bool
	digits string
	exp    int32
}

// EncodeNumericValue returns the order-preserving term for the number s.
//
// s is decimal text: an optional sign, digits with an optional fraction, an
// optional exponent ("-12", "3.25", ".5", "6.02e23"), or an infinity
// ("Inf", "+Inf", "-Infinity", case-insensitive). That covers JSON number
// text and fmt.Sprint of every Go integer and float. NaN, other text, and an
// exponent outside int32 are refused with an InvalidNumberError.
func EncodeNumericValue(s string) ([]byte, error) {
	d, err := parseDecimal(s)
	if err != nil {
		return nil, err
	}
	return d.appendTerm(nil), nil
}

// DecodeNumericValue returns the canonical decimal text of a term written
// by EncodeNumericValue: integers without a fraction or exponent ("123"),
// fractions in plain notation ("-0.25"), and very large or very small
// magnitudes in exponent notation ("1e+21", "1.5e-7"). The text encodes
// back to the same term.
func DecodeNumericValue(term []byte) (string, error) {
	d, n, err := decodeNumericTerm(term, 0)
	if err != nil {
		return "", err
	}
	if n != len(term) {
		return "", &CorruptNumericTermError{Term: term, Reason: "trailing bytes after the term"}
	}
	return d.String(), nil
}

// ResolveNumericScanBound returns the scan bound for raw on a numeric index.
//
// Callers that build half-open range scans [start, end) express "the bound
// is just past this value" -- an exclusive start, or an inclusive end -- by
// appending a single 0x00 byte to the bound's decimal text, the way a
// byte-comparable key is advanced past an exact value. That marker cannot
// survive parsing, so it is stripped first, the remaining text is encoded,
// and the term's successor is returned: the least byte string above every
// key that begins with the term. Because terms are prefix-free, that
// successor is at or below the term of every greater number.
//
// A bound that is not a number, or is NaN, is an InvalidNumericBoundError: a
// malformed bound is a malformed query, never an empty result.
func ResolveNumericScanBound(raw []byte, indexName, op string) ([]byte, error) {
	base := raw
	exclusive := false
	if n := len(raw); n > 0 && raw[n-1] == 0x00 {
		base = raw[:n-1]
		exclusive = true
	}

	encoded, err := EncodeNumericValue(string(base))
	if err != nil {
		return nil, &InvalidNumericBoundError{IndexName: indexName, Op: op, Value: raw, Err: err}
	}
	if exclusive {
		encoded = indexkey.Successor(encoded)
	}
	return encoded, nil
}

// numericTermLen returns the length of the term that starts at data[offset],
// for decoders that find a term inside a longer key.
func numericTermLen(data []byte, offset int) (int, error) {
	_, n, err := decodeNumericTerm(data, offset)
	if err != nil {
		return 0, err
	}
	return n - offset, nil
}

// parseDecimal parses s into its normalized decimal form.
func parseDecimal(s string) (decimal, error) {
	var d decimal
	rest := s
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		d.neg = rest[0] == '-'
		rest = rest[1:]
	}
	if strings.EqualFold(rest, "inf") || strings.EqualFold(rest, "infinity") {
		d.inf = true
		return d, nil
	}
	if strings.EqualFold(rest, "nan") {
		return decimal{}, &InvalidNumberError{Value: s, Reason: NumberIsNaN}
	}

	intEnd := scanDigits(rest, 0)
	intPart := rest[:intEnd]
	i := intEnd
	fracPart := ""
	if i < len(rest) && rest[i] == '.' {
		fracEnd := scanDigits(rest, i+1)
		fracPart = rest[i+1 : fracEnd]
		i = fracEnd
	}
	if intPart == "" && fracPart == "" {
		return decimal{}, &InvalidNumberError{Value: s, Reason: NumberSyntax}
	}

	var exp int64
	if i < len(rest) && (rest[i] == 'e' || rest[i] == 'E') {
		j := i + 1
		expNeg := false
		if j < len(rest) && (rest[j] == '+' || rest[j] == '-') {
			expNeg = rest[j] == '-'
			j++
		}
		expEnd := scanDigits(rest, j)
		if expEnd == j {
			return decimal{}, &InvalidNumberError{Value: s, Reason: NumberSyntax}
		}
		for _, c := range rest[j:expEnd] {
			if exp < numericExponentSaturation {
				exp = exp*10 + int64(c-'0')
			}
		}
		if expNeg {
			exp = -exp
		}
		i = expEnd
	}
	if i != len(rest) {
		return decimal{}, &InvalidNumberError{Value: s, Reason: NumberSyntax}
	}

	mantissa := intPart + fracPart
	lead := 0
	for lead < len(mantissa) && mantissa[lead] == '0' {
		lead++
	}
	digits := strings.TrimRight(mantissa[lead:], "0")
	if digits == "" {
		// Zero, of either sign, is the one zero.
		return decimal{}, nil
	}
	e := int64(len(intPart)) - int64(lead) + exp
	if e < math.MinInt32 || e > math.MaxInt32 {
		return decimal{}, &InvalidNumberError{Value: s, Reason: NumberExponentRange}
	}
	d.digits = digits
	d.exp = int32(e)
	return d, nil
}

// scanDigits returns the index of the first non-digit in s at or after i.
func scanDigits(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

// appendTerm appends d's term to dst.
func (d decimal) appendTerm(dst []byte) []byte {
	switch {
	case d.inf && d.neg:
		return append(dst, numericTagNegInf)
	case d.inf:
		return append(dst, numericTagPosInf)
	case d.digits == "":
		return append(dst, numericTagZero)
	}

	tag := numericTagPos
	if d.neg {
		tag = numericTagNeg
	}
	dst = append(dst, tag)
	body := len(dst)
	dst = binary.BigEndian.AppendUint32(dst, uint32(d.exp)^(1<<31))
	for i := 0; i < len(d.digits); i += 2 {
		b := 1 + numericDigitBase*(d.digits[i]-'0')
		if i+1 < len(d.digits) {
			b += d.digits[i+1] - '0' + 1
		}
		dst = append(dst, b)
	}
	dst = append(dst, numericTerminator)
	if d.neg {
		for i := body; i < len(dst); i++ {
			dst[i] = ^dst[i]
		}
	}
	return dst
}

// decodeNumericTerm decodes the term starting at data[offset] and returns
// it with the offset just past it. Only canonical terms -- the ones
// appendTerm writes -- decode.
func decodeNumericTerm(data []byte, offset int) (decimal, int, error) {
	if offset >= len(data) {
		return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "empty term"}
	}
	var d decimal
	switch data[offset] {
	case numericTagNegInf:
		return decimal{neg: true, inf: true}, offset + 1, nil
	case numericTagPosInf:
		return decimal{inf: true}, offset + 1, nil
	case numericTagZero:
		return decimal{}, offset + 1, nil
	case numericTagNeg:
		d.neg = true
	case numericTagPos:
	default:
		return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "unknown tag"}
	}

	var mask byte
	if d.neg {
		mask = 0xFF
	}
	i := offset + 1
	if len(data)-i < numericExponentLen {
		return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "truncated exponent"}
	}
	expBits := binary.BigEndian.Uint32(data[i : i+numericExponentLen])
	if d.neg {
		expBits = ^expBits
	}
	d.exp = int32(expBits ^ (1 << 31))
	i += numericExponentLen

	digits := make([]byte, 0, 2*(len(data)-i))
	for {
		if i >= len(data) {
			return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "missing terminator"}
		}
		b := data[i] ^ mask
		i++
		if b == numericTerminator {
			break
		}
		if b > numericDigitBase*10 {
			return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "invalid digit byte"}
		}
		hi, lo := (b-1)/numericDigitBase, (b-1)%numericDigitBase
		digits = append(digits, '0'+hi)
		if lo == 0 {
			// A lone digit ends the digits, so the terminator follows.
			if i >= len(data) || data[i]^mask != numericTerminator {
				return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "lone digit before the end"}
			}
			continue
		}
		digits = append(digits, '0'+lo-1)
	}
	if len(digits) == 0 || digits[0] == '0' || digits[len(digits)-1] == '0' {
		return decimal{}, 0, &CorruptNumericTermError{Term: data, Reason: "digits not normalized"}
	}
	d.digits = string(digits)
	return d, i, nil
}

// String returns d's canonical decimal text; see DecodeNumericValue.
func (d decimal) String() string {
	switch {
	case d.inf && d.neg:
		return "-Inf"
	case d.inf:
		return "+Inf"
	case d.digits == "":
		return "0"
	}

	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	n, e := int64(len(d.digits)), int64(d.exp)
	switch {
	case e >= n && e <= 21:
		b.WriteString(d.digits)
		b.WriteString(strings.Repeat("0", int(e-n)))
	case e > 0 && e < n:
		b.WriteString(d.digits[:e])
		b.WriteByte('.')
		b.WriteString(d.digits[e:])
	case e <= 0 && e > -6:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", int(-e)))
		b.WriteString(d.digits)
	default:
		b.WriteByte(d.digits[0])
		if n > 1 {
			b.WriteByte('.')
			b.WriteString(d.digits[1:])
		}
		b.WriteByte('e')
		if e-1 >= 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.FormatInt(e-1, 10))
	}
	return b.String()
}
