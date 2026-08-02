package lint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
