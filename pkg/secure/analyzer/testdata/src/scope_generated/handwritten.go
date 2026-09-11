package scope_generated

// A hand-written file in the SAME PACKAGE as a generated one stays fully in
// scope. The clause keys on the file's generated marker, so a package cannot
// be laundered by putting a holder next to a .pb.go.
type HandwrittenHolder struct {
	KeyMaterial []byte // want `field "KeyMaterial" holds sensitive material as \[\]byte; switch to \*secure\.Secret or \*secure\.Buffer`
}
