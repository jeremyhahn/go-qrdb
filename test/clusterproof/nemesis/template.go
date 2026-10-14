//go:build clusterproof

package nemesis

import (
	"slices"
	"strings"
	"text/template"
)

// ArgvElement is the template element that expands to the inner argv, one
// element per word, the way "$@" does in a shell. It lets a wrapper template
// such as {"docker", "exec", "qrdb-1", "$@"} carry a fault's command without
// re-quoting it.
const ArgvElement = "$@"

// templateFuncs are the functions every command template may call.
var templateFuncs = template.FuncMap{
	// shquote renders a string, or a list of strings joined by spaces, as
	// POSIX-shell-quoted words: for a transport that hands its argv to a
	// remote shell, such as ssh.
	"shquote": func(v any) (string, error) {
		switch w := v.(type) {
		case string:
			return shellQuote([]string{w}), nil
		case []string:
			return shellQuote(w), nil
		}
		return "", &ConfigError{Field: "template", Reason: "shquote takes a string or a list of strings"}
	},
}

// Template is a command template: an argv whose elements are text/template
// texts, executed against a fault's parameters with missing keys an error.
// An element that is exactly [ArgvElement] expands to the inner argv.
type Template struct {
	name  string
	elems []*template.Template // nil where the element is ArgvElement
}

// NewTemplate parses argv as a command template. name identifies the
// template in errors.
func NewTemplate(name string, argv []string) (*Template, error) {
	if len(argv) == 0 {
		return nil, &ConfigError{Field: name, Reason: "command template is empty"}
	}
	t := &Template{name: name, elems: make([]*template.Template, len(argv))}
	for i, src := range argv {
		if src == ArgvElement {
			continue
		}
		el, err := template.New(name).Option("missingkey=error").Funcs(templateFuncs).Parse(src)
		if err != nil {
			return nil, &ConfigError{Field: name, Reason: "command template does not parse", Err: err}
		}
		t.elems[i] = el
	}
	if t.elems[0] == nil {
		return nil, &ConfigError{Field: name, Reason: "command template must not start with " + ArgvElement}
	}
	return t, nil
}

// MustTemplate is NewTemplate for the package's own default templates, which
// are constants: a parse failure is a programming error.
func MustTemplate(name string, argv ...string) *Template {
	t, err := NewTemplate(name, argv)
	if err != nil {
		panic(err)
	}
	return t
}

// Render executes the template against data, expanding [ArgvElement] to
// inner.
func (t *Template) Render(data any, inner []string) ([]string, error) {
	out := make([]string, 0, len(t.elems)+len(inner))
	var b strings.Builder
	for _, el := range t.elems {
		if el == nil {
			out = append(out, inner...)
			continue
		}
		b.Reset()
		if err := el.Execute(&b, data); err != nil {
			return nil, &ConfigError{Field: t.name, Reason: "command template does not render", Err: err}
		}
		out = append(out, b.String())
	}
	return out, nil
}

// takesArgv reports whether the template contains ArgvElement, the only way
// Render's inner argument reaches the command.
func (t *Template) takesArgv() bool {
	return slices.Contains(t.elems, nil)
}

// shellQuote renders words as POSIX-shell single-quoted words joined by
// spaces. A word made only of characters a shell never interprets is left
// bare, so the common case stays readable in logs.
func shellQuote(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quoteWord(w)
	}
	return strings.Join(quoted, " ")
}

func quoteWord(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@%+,") == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// templatePair parses a fault's inject and heal templates, falling back to
// the defaults, and renders both once against sample, so a typo or a missing
// field fails at construction rather than in the middle of a heal.
func templatePair(name string, inject, injectDef, heal, healDef []string, sample any) (*Template, *Template, error) {
	in, err := NewTemplate(name+" inject", orDefault(inject, injectDef))
	if err != nil {
		return nil, nil, err
	}
	out, err := NewTemplate(name+" heal", orDefault(heal, healDef))
	if err != nil {
		return nil, nil, err
	}
	for _, t := range []*Template{in, out} {
		if _, err := t.Render(sample, nil); err != nil {
			return nil, nil, err
		}
	}
	return in, out, nil
}

// orDefault returns argv, or def when argv is empty.
func orDefault(argv, def []string) []string {
	if len(argv) == 0 {
		return def
	}
	return argv
}
