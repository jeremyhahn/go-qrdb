// Package good_ssh_nonident exercises call expressions where the method
// receiver is not a simple identifier (covers the sel.X.(*ast.Ident) !ok
// branch in rule 2 of run).  No diagnostics expected because the receiver
// is a SelectorExpr, not an Ident, so the import-alias check is skipped.
package good_ssh_nonident

// sshProvider wraps a factory that returns a helper with a ParsePrivateKey
// method.  The call site uses a chained selector expression, so the LHS of
// the selector is not a plain *ast.Ident.
type sshHelper struct{}

func (h *sshHelper) ParsePrivateKey(pem []byte) error { return nil }

type provider struct {
	helper sshHelper
}

func use(p provider, pem []byte) {
	// p.helper is a SelectorExpr, not an Ident, so the SSH rule's
	// xIdent, ok := sel.X.(*ast.Ident) assertion fails and the rule
	// returns early without reporting.
	_ = p.helper.ParsePrivateKey(pem)
}
