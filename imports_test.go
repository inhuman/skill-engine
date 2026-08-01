package skillengine_test

import (
	"go/build"
	"strings"
	"testing"
)

// allowedTestDeps — dependencies allowed to TESTS ONLY. Production code must
// have none at all: the engine is embedded into someone else's application, and
// every dependency here becomes a dependency of that embedder — with its
// versions and its conflicts. YAML parsing is taken as a parameter (the
// Unmarshal type), version comparison is implemented in place: all that was
// needed from a semver library was three numbers.
var allowedTestDeps = []string{
	"gopkg.in/yaml.v3",
	"github.com/stretchr/testify/assert",
	"github.com/stretchr/testify/require",
}

// The library must stay self-contained: the skill format and its execution know
// nothing about the embedding application. This is not aesthetics — it is what
// makes it possible to test the format without standing up half of someone
// else's system (the package's tests touch neither MCP, nor a database, nor an
// inference gateway).
//
// Coupling appears unnoticed: a convenient type from a neighbouring package,
// "just one" constant. So the boundary is guarded by a test rather than by an
// agreement.
//
// Both production and test imports are checked: a test that pulled in a
// dependency makes the library heavier just the same.
func TestEngineStaysSelfContained(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}

	// Production code — stdlib only. Not a single exception: as soon as the
	// first one appears, so will the second.
	for _, imp := range pkg.Imports {
		if external(imp) {
			t.Errorf("production code imports %s — the engine must have no dependencies", imp)
		}
	}
	// Tests may use the short list: they do not end up in anyone else's build.
	for _, imports := range [][]string{pkg.TestImports, pkg.XTestImports} {
		for _, imp := range imports {
			if external(imp) && !slicesContains(allowedTestDeps, imp) {
				t.Errorf("a test imports %s — a dependency outside the declared list", imp)
			}
		}
	}
}

// external — an import that is not stdlib (a domain in the first segment) and
// not the engine itself: tests from the external package import it by name, and
// that is not a dependency but a way of exercising the public API from outside.
func external(imp string) bool {
	if imp == selfModule {
		return false
	}
	head, _, _ := strings.Cut(imp, "/")
	return strings.Contains(head, ".")
}

const selfModule = "github.com/inhuman/skill-engine"

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
