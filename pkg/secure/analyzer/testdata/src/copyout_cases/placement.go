package copyout_cases

import "github.com/jeremyhahn/go-qrdb/pkg/secure"

// --- Placement: several values, var declarations, positional returns. -----

func multiAssign(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		n, out := 1, append([]byte(nil), b...)
		defer secure.Wipe(out)
		use(out[:n])
		return nil
	})
}

func varSingle(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		var out = append([]byte(nil), b...)
		secure.Wipe(out)
		return nil
	})
}

func varMulti(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		var n, out = 1, append([]byte(nil), b...) // want `append\(\.\.\., view\.\.\.\) copies protected bytes`
		use(out[:n])
		return nil
	})
}

func returnsSecond(buf *secure.Buffer) (int, []byte) { // want returnsSecond:"returnsCopy\\(1\\)"
	var out []byte
	_ = buf.WithBytes(func(b []byte) error {
		out = append([]byte(nil), b...)
		return nil
	})
	return len(out), out
}

// A copy passed straight to an ordinary function is reported.
func passedToOrdinary(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		use(append([]byte(nil), b...)) // want `append\(\.\.\., view\.\.\.\) copies protected bytes`
		return nil
	})
}

// --- Declared consumers and the shapes that are not views. ---------------

var keptForLater []byte

// takeOwnership keeps b and wipes it on its own schedule.
//
//secure:consumes
func takeOwnership(b []byte) { // want takeOwnership:"consumes\\(0\\)"
	keptForLater = b
}

func consumedByDeclaredFunc(buf *secure.Buffer) {
	out, _ := secure.CopyOut(buf)
	takeOwnership(out)
}

type stringBorrower struct{}

func (stringBorrower) WithBytes(fn func(string) error) error { return fn("") }

// A WithBytes whose callback does not take []byte yields no view.
func notAByteCallback(s stringBorrower) {
	_ = s.WithBytes(func(v string) error {
		use([]byte(v))
		return nil
	})
}

// A callback passed by variable is not analysed as a view (a documented limit).
func callbackByVariable(buf *secure.Buffer) {
	fn := func(b []byte) error {
		use(append([]byte(nil), b...))
		return nil
	}
	_ = buf.WithBytes(fn)
}

func twoWipers() (func([]byte), func([]byte)) { return secure.Wipe, secure.Wipe }

// An initialiser the analysis cannot line up to one function carries no fact.
var wipeA, wipeB = twoWipers()
