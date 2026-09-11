// Package scope_test_file exercises S4: rule 3 does not apply in _test.go
// files, and rules 1 and 2 still do.
package scope_test_file

// A holder in a NON-test file is fully in scope. This is the floor that keeps
// "move it into a helper" from being a way out of the rule — only the
// _test.go suffix is out of scope, not "test-ish" code.
type ProductionHolder struct {
	KeyMaterial []byte // want `field "KeyMaterial" holds sensitive material as \[\]byte; switch to \*secure\.Secret or \*secure\.Buffer`
}
