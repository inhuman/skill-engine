package lint_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/inhuman/skill-engine/lint"
)

// The fixtures are broken skills by class of defect, and the expected rule set
// is written out per file. Both directions matter: a rule that stopped firing
// is a rule that silently went missing, and a rule that fires where it was not
// expected is the noise that stops the report being read.
var fixtureRules = map[string][]string{
	"prose-defects.yaml":    {"S3", "S6", "E5"},
	"dead-server.yaml":      {"E1", "E2", "E3"},
	"broken-program.yaml":   {"W1"},
	"silent-loop.yaml":      {"W2", "W6", "W12", "W14"},
	"envelope.yaml":         {"W3", "W8", "W10"},
	"loose-schema.yaml":     {"W9", "W13", "W16"},
	"payload.yaml":          {"W4", "W5", "W7", "W11", "W15", "W17", "E4"},
	"dead-alternative.yaml": {"W18"},
}

func lintFixture(t *testing.T, name string) lint.Report {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("fixtures", name))
	require.NoError(t, err)
	rep, err := lint.LintAll([]lint.Source{{Path: name, Raw: raw}}, testFacts(), testOptions())
	require.NoError(t, err)
	return rep
}

func TestFixturesAreCaughtByTheirOwnRules(t *testing.T) {
	for name, want := range fixtureRules {
		t.Run(name, func(t *testing.T) {
			rep := lintFixture(t, name)
			got := rulesIn(rep)
			sort.Strings(want)
			assert.Equal(t, want, got, "the fixture's findings changed:\n%s", rep.Text())
		})
	}
}

// Every fixture is a real file, and every file has a fixture: an unreferenced
// fixture is one nobody notices has stopped working.
func TestEveryFixtureIsListed(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("fixtures", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, path := range files {
		name := filepath.Base(path)
		assert.Containsf(t, fixtureRules, name, "fixture %s is not listed in fixtureRules", name)
	}
	assert.Len(t, fixtureRules, len(files))
}

// The catalogue is what an embedder reads instead of the source. A rule listed
// there and firing nowhere has either been renamed or quietly lost its call
// site — and either way the entry is a promise the package does not keep.
func TestEveryCatalogueRuleFires(t *testing.T) {
	seen := map[string]bool{}
	for name := range fixtureRules {
		for _, rule := range rulesIn(lintFixture(t, name)) {
			seen[rule] = true
		}
	}
	// X1 is about a rule NOT running, so it needs a run with nothing to run on.
	rep, err := lint.Lint([]byte(head+"workflow:\n  steps:\n    - name: s\n      call: {tool: \"docs:page_get\", args: {}, save_as: p}\n"),
		lint.Facts{}, lint.Options{Unmarshal: yaml.Unmarshal})
	require.NoError(t, err)
	for _, rule := range rulesIn(rep) {
		seen[rule] = true
	}
	// S1 and S5 need a defect the fixtures deliberately do not carry: every
	// fixture is a loadable file, and none of them is oversized.
	for _, extra := range []struct{ rule, src string }{
		{"S1", "name: [unclosed\n"},
		{"S5", head + "playbook: |\n  " + longLine(lint.DefaultPlaybookBudget) + "\n"},
	} {
		rep, err := lint.Lint([]byte(extra.src), testFacts(), testOptions())
		require.NoError(t, err)
		for _, rule := range rulesIn(rep) {
			seen[rule] = true
		}
	}

	for _, rule := range lint.Rules() {
		assert.Truef(t, seen[rule.ID], "rule %s is in the catalogue and fires nowhere: %s", rule.ID, rule.Title)
	}
}

// The other direction: a rule the code emits and the catalogue does not list is
// a rule an embedder cannot look up, and a number nobody owns.
func TestEveryEmittedRuleIsCatalogued(t *testing.T) {
	catalogued := map[string]bool{}
	for _, rule := range lint.Rules() {
		catalogued[rule.ID] = true
	}
	for name := range fixtureRules {
		for _, rule := range rulesIn(lintFixture(t, name)) {
			assert.Truef(t, catalogued[rule], "rule %s is emitted and not in the catalogue", rule)
		}
	}
}

func TestCatalogueIdsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, rule := range lint.Rules() {
		assert.Falsef(t, seen[rule.ID], "the catalogue lists %s twice", rule.ID)
		seen[rule.ID] = true
		assert.NotEmptyf(t, rule.Title, "%s has no title", rule.ID)
		assert.NotEmptyf(t, rule.Emits, "%s says nothing about what it emits", rule.ID)
	}
}

// The shipped examples are what people read the format from. They must come out
// clean — an example that trips a rule teaches the defect along with the format,
// and this is also the false-positive guard for every rule at once.
func TestShippedExamplesAreClean(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "the examples are gone")

	opts := testOptions()
	// The examples are written against a vocabulary of their own — they name
	// their servers and tools freely, so the live facts are not asked for here.
	opts.CallProtocol = ""
	opts.StaleAPIs = nil
	opts.HostVars = []string{"input", "history"}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var doc struct {
				Name string `yaml:"name"`
			}
			require.NoError(t, yaml.Unmarshal(raw, &doc))
			if doc.Name == "" {
				t.Skip("a vocabulary of values, not a skill")
			}
			rep, err := lint.LintAll([]lint.Source{{Path: filepath.Base(path), Raw: raw}}, lint.Facts{}, opts)
			require.NoError(t, err)

			for _, f := range rep.Findings {
				if f.Rule == lint.SkipRule || f.Severity == lint.SeverityInfo {
					continue // a rule that could not run is not a defect of the example
				}
				assert.Failf(t, "an example is not clean", "%s: [%s] %s: %s", filepath.Base(path), f.Severity, f.Rule, f.Message)
			}
		})
	}
}

func rulesIn(rep lint.Report) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range rep.Findings {
		if seen[f.Rule] {
			continue
		}
		seen[f.Rule] = true
		out = append(out, f.Rule)
	}
	sort.Strings(out)
	return out
}

func longLine(n int) string {
	b := make([]byte, n+1)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
