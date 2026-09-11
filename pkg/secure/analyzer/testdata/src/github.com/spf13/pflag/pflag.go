// Minimal stub for analyzer testdata. Not a functional implementation.
package pflag

type FlagSet struct{}

func (f *FlagSet) StringVar(p *string, name, value, usage string)                 {}
func (f *FlagSet) StringVarP(p *string, name, shorthand, value, usage string)     {}
func (f *FlagSet) BytesHexVar(p *[]byte, name string, value []byte, usage string) {}

// ResetVar takes no arguments. Real pflag has no such method; it exists here
// to drive the zero-argument guard in flagBoundFields.
func (f *FlagSet) ResetVar() {}

// NoPointerVar takes a non-address first argument, and IntVar takes one that
// is a unary expression with the wrong operator. Both drive guards in
// flagBoundFields that real pflag call sites never reach.
func NoPointerVar(name string) {}
func IntVar(n int)             {}

// SomeGlobal is a package-level variable, so &pflag.SomeGlobal is a selector
// whose object is a *types.Var that is NOT a field.
var SomeGlobal string
