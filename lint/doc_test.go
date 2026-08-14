package lint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	skillengine "github.com/inhuman/skill-engine"
	"github.com/inhuman/skill-engine/lint"
)

// The README's table is what a person reads to find out what the linter does.
// A rule missing from it is a rule nobody knows exists — which is the same as
// not having written it, only more expensive.
func TestReadmeListsEveryRule(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	require.NoError(t, err)
	doc := string(raw)

	for _, rule := range lint.Rules() {
		assert.Containsf(t, doc, "| "+rule.ID+" |", "rule %s is not in the README's table", rule.ID)
	}
}

// A rule pointing at a handbook section that does not exist is worse than a
// rule pointing nowhere: the refusal names a place, the reader goes, and finds
// nothing. Sections get renamed by whoever edits the handbook, and this is what
// tells them a rule was left behind.
func TestEveryRulePointsAtASectionThatExists(t *testing.T) {
	sections := map[string]bool{}
	for _, s := range skillengine.HandbookIndex() {
		sections[s.ID] = true
	}
	require.NotEmpty(t, sections)

	for _, rule := range lint.Rules() {
		if rule.Handbook == "" {
			continue
		}
		assert.Truef(t, sections[rule.Handbook],
			"rule %s points at handbook section %q, which the module does not ship", rule.ID, rule.Handbook)
		assert.NotEmpty(t, skillengine.Handbook(rule.Handbook))
	}
}

// And the pointer has to reach the finding: a mapping invisible from a report
// is a mapping that does nothing.
func TestAFindingCarriesItsHandbookSection(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: tell
      instruction: "retell {{nope}}"
      tools: []
`))
	f := requireFinding(t, rep, "W14", lint.SeverityError)
	assert.Equal(t, "instruction-text", f.Handbook)
}

// The same in the other direction for the fixtures: an unlisted file is one
// nobody notices has stopped working.
func TestFixturesReadmeListsEveryFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("fixtures", "README.md"))
	require.NoError(t, err)
	doc := string(raw)

	files, err := filepath.Glob(filepath.Join("fixtures", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, path := range files {
		name := filepath.Base(path)
		assert.Containsf(t, doc, "`"+name+"`", "fixture %s is not in the fixtures README", name)
	}
}
