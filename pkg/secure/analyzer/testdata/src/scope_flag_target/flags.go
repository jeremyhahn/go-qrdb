// Package scope_flag_target exercises S3, the command-line flag clause: a
// field whose ADDRESS reaches a flag binder is out of scope for rule 3
// because os.Args already holds the plaintext unwipeably.
package scope_flag_target

import (
	"flag"

	"github.com/spf13/pflag"
)

// InitFlags is the shape of cmd/qrdb/barrier.go's flag structs.
type InitFlags struct {
	strategy   string
	passphrase string
	// A sensitive field in the SAME STRUCT that no binder writes is still
	// reported. Detection is by binding, not by the struct it sits in.
	Secret string // want `field "Secret" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
}

func bindPflag(fs *pflag.FlagSet, f *InitFlags) {
	fs.StringVar(&f.passphrase, "passphrase", "", "Master passphrase")
	fs.StringVar(&f.strategy, "barrier-strategy", "passphrase", "Barrier strategy")
}

// StdlibFlags is bound through the standard library's flag package rather
// than pflag. Both spell the binder `<Type>Var`, and both are in scope for
// the clause.
type StdlibFlags struct {
	Password string
}

func bindStdlib(f *StdlibFlags) {
	flag.StringVar(&f.Password, "password", "", "Password")
}

// ShorthandFlags is bound through pflag's `P` shorthand form.
type ShorthandFlags struct {
	PIN string
}

func bindShorthand(fs *pflag.FlagSet, f *ShorthandFlags) {
	fs.StringVarP(&f.PIN, "pin", "p", "", "PIN")
}

// NotAFlagSet has a method whose name ends in Var but which has nothing to do
// with flags. The receiver check is what keeps the name suffix from matching
// it.
type NotAFlagSet struct{}

func (NotAFlagSet) StringVar(p *string, name string) {}

type ImposterBound struct {
	KeyMaterial string // want `field "KeyMaterial" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
}

func bindImposter(n NotAFlagSet, f *ImposterBound) {
	n.StringVar(&f.KeyMaterial, "key-material")
}

// ---------------------------------------------------------------------------
// Shapes that reach a flag binder's NAME but not a field binding. Every one of
// them must leave rule 3 in force, because none of them establishes that the
// field is written from os.Args.
// ---------------------------------------------------------------------------

// FunctionValuedField holds a binder in a struct FIELD rather than on a named
// type, so the receiver is an anonymous struct and not a *types.Named.
type FunctionValuedField struct {
	StringVar func(p *string, name string)
}

type ImposterFieldBound struct {
	RecoveryShare string // want `field "RecoveryShare" holds sensitive material as string; switch to \*secure\.Secret or \*secure\.Buffer`
}

func bindThroughAField(b FunctionValuedField, f *ImposterFieldBound) {
	b.StringVar(&f.RecoveryShare, "recovery-share")
}

// packageLevelPassphrase is bound to a flag, but it is a package variable and
// not a struct field, so nothing about it is in rule 3's subject at all.
var packageLevelPassphrase string

func bindTheOddShapes(fs *pflag.FlagSet) {
	// Zero arguments: nothing to inspect.
	fs.ResetVar()
	// A first argument that is not an address.
	pflag.NoPointerVar("passphrase")
	// A first argument that is a unary expression with the wrong operator.
	pflag.IntVar(-1)
	// An address of a plain identifier rather than of a selector.
	flag.StringVar(&packageLevelPassphrase, "passphrase", "", "passphrase")
	// An address of a selector whose object is a package variable, not a field.
	flag.StringVar(&pflag.SomeGlobal, "some-global", "", "not a field")
}
