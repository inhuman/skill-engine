package skillengine_test

import (
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
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
// Every directory of the module is checked, not only this one: a dependency
// pulled into a subpackage is a dependency of anyone who imports it, and the
// rule would otherwise hold for exactly as long as the module had one package.
func TestEngineStaysSelfContained(t *testing.T) {
	for _, dir := range packageDirs(t) {
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatalf("parsing the package in %s: %v", dir, err)
		}

		// Production code — stdlib only. Not a single exception: as soon as the
		// first one appears, so will the second.
		for _, imp := range pkg.Imports {
			if external(imp) {
				t.Errorf("%s: production code imports %s — the engine must have no dependencies", dir, imp)
			}
		}
		// Tests may use the short list: they do not end up in anyone else's build.
		for _, imports := range [][]string{pkg.TestImports, pkg.XTestImports} {
			for _, imp := range imports {
				if external(imp) && !slicesContains(allowedTestDeps, imp) {
					t.Errorf("%s: a test imports %s — a dependency outside the declared list", dir, imp)
				}
			}
		}
	}
}

// packageDirs lists the module's package directories by walking, rather than by
// a written-out list: a list is what the next package gets left out of.
func packageDirs(t *testing.T) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if name := d.Name(); path != "." && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return fs.SkipDir
		}
		// A directory with its own go.mod is a DIFFERENT module and not part of
		// this one. That is what lets the examples show the engine embedded in
		// an application built on a framework — eino appears in that module's
		// go.mod and never in the engine's.
		//
		// The guard is not weakened by this: delete such a go.mod and the files
		// join this module, the walk finds them again, and their imports are
		// reported like anyone else's.
		if path != "." {
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return fs.SkipDir
			}
		}
		if files, _ := filepath.Glob(filepath.Join(path, "*.go")); len(files) > 0 {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if len(dirs) < 2 {
		t.Fatalf("only %d package directory found — the walk stopped seeing the module", len(dirs))
	}
	return dirs
}

// external — an import that is not stdlib (a domain in the first segment) and
// not the module itself: tests from the external package import it by name, and
// the linter imports the engine — neither is a dependency, both are the module
// using its own public API.
func external(imp string) bool {
	if imp == selfModule || strings.HasPrefix(imp, selfModule+"/") {
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

// The examples must stay SEPARATE modules, and this is the guard for that.
//
// It is the only thing standing between "an example shows the engine used with
// a framework" and "the engine depends on a framework": delete one of these
// go.mod files and every import inside becomes an import of this module, which
// TestEngineStaysSelfContained would then report — but only if somebody
// remembers the example was supposed to be its own module in the first place.
// This says so out loud.
func TestExampleAppsAreSeparateModules(t *testing.T) {
	entries, err := os.ReadDir("examples")
	if err != nil {
		t.Fatalf("reading examples: %v", err)
	}
	var apps int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("examples", e.Name())
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		if len(files) == 0 {
			continue // examples/skills — YAML, part of no module
		}
		apps++
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			t.Errorf("%s holds Go code and no go.mod — its dependencies would become the engine's", dir)
		}
	}
	if apps == 0 {
		t.Fatal("no example application found — the walk is looking at the wrong place")
	}
}
