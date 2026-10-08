package copyout_cases

import (
	"bytes"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

func use([]byte) {}

// --- Reported: a copy nothing wipes, returns or consumes. -----------------

// The #429 shape: a copy taken inside the borrow and left to the GC.
func unwipedAppend(buf *secure.Buffer) {
	var out []byte
	_ = buf.WithBytes(func(b []byte) error {
		out = append([]byte(nil), b...) // want `append\(\.\.\., view\.\.\.\) copies protected bytes into ordinary memory that is never wiped`
		return nil
	})
	use(out)
}

func unwipedCopy(buf *secure.Buffer, n int) {
	dst := make([]byte, n)
	_ = buf.WithBytes(func(b []byte) error {
		copy(dst, b) // want `copy\(dst, view\) copies protected bytes`
		return nil
	})
	use(dst)
}

func unwipedClone(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		c := bytes.Clone(b) // want `bytes\.Clone\(view\) copies protected bytes`
		use(c)
		return nil
	})
}

type holder struct{ key []byte }

// Straight into a field: there is no local variable to wipe.
func intoField(buf *secure.Buffer, h *holder) {
	_ = buf.WithBytes(func(b []byte) error {
		h.key = append([]byte(nil), b...) // want `append\(\.\.\., view\.\.\.\) copies protected bytes`
		return nil
	})
}

// A string can never be wiped.
func toString(buf *secure.Buffer) string {
	var s string
	_ = buf.WithBytes(func(b []byte) error {
		s = string(b) // want `string\(\.\.\.\) converts protected bytes to a string, which can never be wiped`
		return nil
	})
	return s
}

// A caller of a function that returns a copy owns it.
func callerDrops(buf *secure.Buffer) {
	out, err := secure.CopyOut(buf) // want `CopyOut\(\.\.\.\) copies protected bytes`
	if err != nil {
		return
	}
	use(out)
}

// A closure that returns a copy hands it to a caller the analysis cannot see.
func closureReturns(buf *secure.Buffer) func() []byte {
	return func() []byte {
		out, _ := secure.CopyOut(buf) // want `CopyOut\(\.\.\.\) copies protected bytes`
		return out
	}
}

// A wipe through a SECOND variable is not followed: reported, in the safe
// direction.
func aliasWipe(buf *secure.Buffer) {
	out, _ := secure.CopyOut(buf) // want `CopyOut\(\.\.\.\) copies protected bytes`
	alias := out
	secure.Wipe(alias)
}

// A deferred call evaluates its argument when the defer runs: written before
// the copy, it wipes the nil slice out held then, never the copy.
func deferredTooEarly(buf *secure.Buffer) {
	var out []byte
	defer secure.Wipe(out)
	_ = buf.WithBytes(func(b []byte) error {
		out = append(out, b...) // want `append\(\.\.\., view\.\.\.\) copies protected bytes`
		return nil
	})
	use(out)
}

// A wipe that runs before the copy is no wipe of it.
func wipedTooEarly(buf *secure.Buffer) {
	var out []byte
	secure.Wipe(out)
	out, _ = secure.CopyOut(buf) // want `CopyOut\(\.\.\.\) copies protected bytes`
	use(out)
}

// --- Silent: the obligation is discharged. --------------------------------

func deferredWipe(buf *secure.Buffer) {
	var out []byte
	_ = buf.WithBytes(func(b []byte) error {
		out = append(out, b...)
		return nil
	})
	defer secure.Wipe(out)
	use(out)
}

// A deferred closure reads out at exit, so it counts wherever it is written.
func deferredClosureBeforeTheCopy(buf *secure.Buffer) {
	var out []byte
	defer func() { secure.Wipe(out) }()
	_ = buf.WithBytes(func(b []byte) error {
		out = append(out, b...)
		return nil
	})
	use(out)
}

func wipedInClosure(buf *secure.Buffer) {
	out, _ := secure.CopyOut(buf)
	defer func() { secure.Wipe(out) }()
	use(out)
}

func wipedThroughReslice(buf *secure.Buffer) {
	out, _ := secure.CopyOut(buf)
	use(out)
	secure.Wipe(out[:len(out)])
}

func clearedBuiltin(buf *secure.Buffer) {
	out, _ := secure.CopyOut(buf)
	use(out)
	clear(out)
}

func consumedByConstructor(buf *secure.Buffer) (*secure.Buffer, error) {
	out, err := secure.CopyOut(buf)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(out)
}

func consumedDirectly(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		secure.Wipe(append([]byte(nil), b...))
		return nil
	})
}

// Returning moves the obligation to the caller, as a fact.
func returnsIt(buf *secure.Buffer) ([]byte, error) { // want returnsIt:"returnsCopy\\(0\\)"
	return secure.CopyOut(buf)
}

func returnsNamed(buf *secure.Buffer) (out []byte, err error) { // want returnsNamed:"returnsCopy\\(0\\)"
	out, err = secure.CopyOut(buf)
	return
}

func callerWipesTransitively(buf *secure.Buffer) {
	out, _ := returnsIt(buf)
	defer secure.Wipe(out)
	use(out)
}

// Copying between two views stays in protected memory.
func viewToView(dst, src *secure.Buffer) {
	_ = dst.WithBytes(func(d []byte) error {
		return src.WithBytes(func(s []byte) error {
			copy(d, s)
			return nil
		})
	})
}

// A package-level function variable means what its initialiser means.
var wipeVia = secure.Wipe // want wipeVia:"consumes\\(0\\)"

func consumedThroughVar(buf *secure.Buffer) {
	out, _ := secure.CopyOut(buf)
	defer wipeVia(out)
	use(out)
}

// A contract inference cannot see is declared.
type sink interface {
	// Take owns b and wipes it once delivered.
	//
	//secure:consumes
	Take(b []byte) // want Take:"consumes\\(0\\)"
}

func consumedByDeclaredInterface(buf *secure.Buffer, s sink) {
	out, _ := secure.CopyOut(buf)
	s.Take(out)
}

// A copy that must outlive every wipe says why, at the site.
func justified(buf *secure.Buffer, h *holder) {
	_ = buf.WithBytes(func(b []byte) error {
		//secure:unwiped the holder is wiped by its own Close, which this analysis cannot follow
		h.key = append([]byte(nil), b...)
		return nil
	})
}

// A directive trailing a DIFFERENT statement on the line above belongs to that
// statement and justifies nothing below it.
func justifiedByATrailingComment(buf *secure.Buffer, h *holder) {
	_ = buf.WithBytes(func(b []byte) error {
		n := 0                                //secure:unwiped this trails n, not the copy below
		h.key = append([]byte(nil), b[n:]...) // want `append\(\.\.\., view\.\.\.\) copies protected bytes`
		return nil
	})
}

// A directive with no reason justifies nothing.
func justifiedWithoutReason(buf *secure.Buffer, h *holder) {
	_ = buf.WithBytes(func(b []byte) error {
		//secure:unwiped
		h.key = append([]byte(nil), b...) // want `append\(\.\.\., view\.\.\.\) copies protected bytes`
		return nil
	})
}

// Passing a VIEW to a callee is not a copy-out: what the callee does with it
// is not followed (a documented limit).
func viewToCallee(buf *secure.Buffer) {
	_ = buf.WithBytes(func(b []byte) error {
		use(b)
		return nil
	})
}
