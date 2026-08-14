package skillengine_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allowedVarsAccess — the functions permitted to touch the variable map by key.
// Everything else goes through a resolver.
//
// Deliberately short, and each entry is here because it IS the mechanism rather
// than a consumer of it:
//
//	resolve  — the resolver itself: the one place that reads a variable, and
//	           the only one that knows a name may be a path into its value;
//	set      — the writer, the counterpart of resolve;
//	newState — seeds the variables that came in, before any step runs.
var allowedVarsAccess = map[string]bool{
	"resolve":  true,
	"set":      true,
	"newState": true,
}

// One resolver per reference.
//
// A variable holds what the host would show the MODEL: a large tool result
// arrives as a preview with a "[mem:id]" handle appended. Whoever wants the
// DATA — a tool argument, a loop's collection, a condition — needs the whole
// thing with the note stripped, and every consumer used to sort that out for
// itself. One of them always forgot: the class fired four times in a single day
// at an embedder, each time somewhere new — a field that would not parse
// because of the note, a loop walking a preview instead of the full list, the
// note reaching a script's stdin.
//
// The fix in each place took a minute; the rule is what has to survive, and a
// rule without a guard lasts until the next consumer. Hence this test rather
// than a paragraph in the README — the same technique imports_test.go already
// uses to guard the zero-dependency promise.
//
// A new consumer must pick a form out loud: `expand` for the model (preview,
// note kept), `payload` for everyone else (whole, note stripped).
func TestVariableAccessGoesThroughAResolver(t *testing.T) {
	for _, path := range productionFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err, "parsing %s", path)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || allowedVarsAccess[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				idx, ok := n.(*ast.IndexExpr)
				if !ok {
					return true
				}
				sel, ok := idx.X.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "vars" {
					return true
				}
				t.Errorf("%s: %s touches the variable map by key — go through the seam: "+
					"expand() for the model (preview, note kept), payload() for data (whole, note stripped), set() to write",
					fset.Position(idx.Pos()), fn.Name.Name)
				return true
			})
		}
	}
}

// productionFiles — the package's own .go files, tests excluded: a test may
// legitimately build a state by hand to exercise the resolver itself.
func productionFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, all)

	var out []string
	for _, path := range all {
		if !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
	}
	require.NotEmpty(t, out, "no production files found — the guard would pass vacuously")
	return out
}
