package skillengine_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

// A whole 1.x skill, with the two things a textual migration can ruin: comments
// that explain the skill, and a script inside a block scalar.
const legacySkill = `# An expense report: fetch, compute IN CODE, draw a chart.
#
# The comments are the reason the fields exist — a migration that drops them
# has traded one silent loss for another.

skill_engine_version: "1.0.0"
skill_version: "1.0.0"
name: expenses
description: Show the spending for a period as a chart.

servers: [ledger, sandbox]

workflow:
  tools: [ledger, sandbox]

  assets:
    chart:
      kind: code
      lang: python      # the language, for the linter
      description: a bar chart
      content: |
        # this script is data, not YAML:
        lang: not a key
        params: not a key either
        print("done")

    notes:
      kind: text
      content: "no lang here"

  steps:
    - name: draw
      call:
        tool: run
        args:
          code: {from: "asset:chart"}
      on_server: sandbox
      save_as: picture
`

func TestMigrateRewritesAssetLang(t *testing.T) {
	out, changed, err := se.Migrate([]byte(legacySkill))
	require.NoError(t, err)
	require.True(t, changed)

	got := string(out)
	assert.Contains(t, got, "      params:\n        lang: python      # the language, for the linter",
		"lang moved into params keeping its indentation and its comment")
	assert.NotContains(t, got, "\n      lang: python", "the old field is gone")
	assert.Contains(t, got, `skill_engine_version: "2.0.0"`, "the declared format moved to the current major")
}

// The script inside `content: |` is data. A line there reading `lang:` is not a
// YAML key, and rewriting it would corrupt the very asset the migration is
// supposed to carry across.
func TestMigrateLeavesBlockScalarsAlone(t *testing.T) {
	out, _, err := se.Migrate([]byte(legacySkill))
	require.NoError(t, err)

	got := string(out)
	assert.Contains(t, got, "        lang: not a key", "the script's line was rewritten as if it were a field")
	assert.Contains(t, got, "        params: not a key either")
	assert.Contains(t, got, `        print("done")`)
}

func TestMigrateKeepsComments(t *testing.T) {
	out, _, err := se.Migrate([]byte(legacySkill))
	require.NoError(t, err)

	got := string(out)
	for _, want := range []string{
		"# An expense report: fetch, compute IN CODE, draw a chart.",
		"# The comments are the reason the fields exist",
		"# the language, for the linter",
	} {
		assert.Contains(t, got, want, "a comment was lost")
	}
}

// The point of migrating: the result must load under this engine.
func TestMigratedSkillLoadsAndValidates(t *testing.T) {
	out, _, err := se.Migrate([]byte(legacySkill))
	require.NoError(t, err)

	var doc struct {
		Version  string    `yaml:"skill_engine_version"`
		Workflow yaml.Node `yaml:"workflow"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc), "the migrated file does not parse")
	require.NoError(t, se.CheckEngineVersion(doc.Version), "the engine still refuses the migrated skill")

	var f se.Flow
	require.NoError(t, doc.Workflow.Decode(&f))
	require.NoError(t, f.Validate())
	assert.Equal(t, "python", f.Assets["chart"].Params["lang"], "the language did not survive as a param")
	assert.Equal(t, "code", f.Assets["chart"].Kind)
}

// The same file twice must not grow a second `params:`.
func TestMigrateIsIdempotent(t *testing.T) {
	once, changed, err := se.Migrate([]byte(legacySkill))
	require.NoError(t, err)
	require.True(t, changed)

	twice, changed, err := se.Migrate(once)
	require.NoError(t, err)
	assert.False(t, changed, "a skill already on the current major was rewritten again")
	assert.Equal(t, string(once), string(twice))
}

// A skill that never declared a version is a 1.x skill that stayed quiet. It
// must come out saying which format it is in now — otherwise the next engine
// reads it as legacy again.
func TestMigrateAddsMissingVersion(t *testing.T) {
	src := "# a skill from before the field existed\nname: old\ndescription: does something\nplaybook: |\n  do the thing\n"
	out, changed, err := se.Migrate([]byte(src))
	require.NoError(t, err)
	require.True(t, changed)

	got := string(out)
	assert.Contains(t, got, `skill_engine_version: "2.0.0"`)
	assert.True(t, strings.HasPrefix(got, "# a skill from before the field existed\n"),
		"the header comment must stay on top")
	assert.Contains(t, got, "name: old")

	var doc struct {
		Version string `yaml:"skill_engine_version"`
		Name    string `yaml:"name"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Equal(t, "2.0.0", doc.Version)
	assert.Equal(t, "old", doc.Name)
}

// A `lang` nested inside an asset's `args` belongs to a tool call, not to the
// asset. Only a DIRECT field is the one 2.0.0 moved.
func TestMigrateIgnoresNestedLang(t *testing.T) {
	src := `skill_engine_version: "1.0.0"
name: fetcher
description: pulls a page
workflow:
  assets:
    page:
      kind: text
      source: mcp
      ref: "wiki:get_page"
      args:
        title: "Home"
        lang: ru
  steps:
    - name: s
      instruction: "{{asset:page}}"
      tools: []
`
	out, _, err := se.Migrate([]byte(src))
	require.NoError(t, err)

	got := string(out)
	assert.Contains(t, got, "        lang: ru", "a nested args key was moved as if it were the asset's own")
	assert.NotContains(t, got, "params:", "nothing to migrate here")

	var doc struct {
		Workflow yaml.Node `yaml:"workflow"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc))
	var f se.Flow
	require.NoError(t, doc.Workflow.Decode(&f))
	assert.Equal(t, "ru", f.Assets["page"].Args["lang"], "the call argument was disturbed")
}

func TestMigrateRefusesToGuess(t *testing.T) {
	t.Run("both lang and params", func(t *testing.T) {
		src := `skill_engine_version: "1.0.0"
name: mixed
description: hand-edited
workflow:
  assets:
    a:
      kind: code
      lang: python
      params: {lang: sql}
      content: "x"
  steps:
    - name: s
      instruction: x
      tools: []
`
		_, _, err := se.Migrate([]byte(src))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "merge them by hand")
	})

	t.Run("from a newer major", func(t *testing.T) {
		_, _, err := se.Migrate([]byte("skill_engine_version: \"9.0.0\"\nname: future\ndescription: x\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "newer major")
	})

	t.Run("not semver", func(t *testing.T) {
		_, _, err := se.Migrate([]byte("skill_engine_version: \"tomorrow\"\nname: x\ndescription: y\n"))
		require.Error(t, err)
	})
}

// Input that is not one YAML document is refused.
//
// The failure this prevents looked like success: given a file in front matter
// the migration inserted the version field ahead of the opening marker, the
// header stopped being a header, the skill lost its name — and the caller got
// changed=true with no error. The wrapper belongs to the embedder, so the
// library refuses instead of learning about it.
func TestMigrateRefusesMultiDocument(t *testing.T) {
	for name, src := range map[string]string{
		"front matter and a markdown body": "---\nname: pods\ndescription: show pods\n---\n\n# Pods\n\nThe body the host wraps around it.\n",
		"two documents":                    "name: first\ndescription: x\n---\nname: second\ndescription: y\n",
		"an explicit end marker":           "---\nname: pods\ndescription: x\n...\n",
		"a separator after content":        "name: pods\ndescription: x\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			out, changed, err := se.Migrate([]byte(src))
			require.Error(t, err, "a wrapped file was migrated as if it were a skill")
			assert.Contains(t, err.Error(), "not a single YAML document")
			assert.Nil(t, out, "nothing is handed back on a refusal")
			assert.False(t, changed)
		})
	}
}

// An explicit start of a SINGLE document is ordinary YAML and stays supported —
// and the inserted version field must land after the marker, not ahead of it,
// or the migration would create the very second document it refuses to accept.
func TestMigrateKeepsExplicitDocumentStart(t *testing.T) {
	out, changed, err := se.Migrate([]byte("---\nname: pods\ndescription: show pods\nplaybook: |\n  do the thing\n"))
	require.NoError(t, err)
	require.True(t, changed)

	assert.Equal(t, "---\nskill_engine_version: \"2.0.0\"\nname: pods\ndescription: show pods\nplaybook: |\n  do the thing\n", string(out))

	var doc struct {
		Version string `yaml:"skill_engine_version"`
		Name    string `yaml:"name"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc), "the result stopped being one document")
	assert.Equal(t, "2.0.0", doc.Version)
	assert.Equal(t, "pods", doc.Name, "the name was lost — the field landed in the wrong document")
}

// A separator inside a block scalar is part of a script, not a document
// boundary. Refusing here would reject perfectly good skills: `---` is ordinary
// text in a heredoc, a markdown template or a YAML-emitting snippet.
func TestMigrateAllowsSeparatorInsideBlockScalar(t *testing.T) {
	src := `skill_engine_version: "1.0.0"
name: emitter
description: writes yaml
workflow:
  assets:
    tpl:
      kind: code
      lang: bash
      content: |
        cat <<'EOF'
        ---
        a: 1
        ---
        b: 2
        EOF
  steps:
    - name: s
      call: {tool: "srv:run", save_as: out}
`
	out, changed, err := se.Migrate([]byte(src))
	require.NoError(t, err, "a separator inside a script was taken for a document boundary")
	require.True(t, changed)

	got := string(out)
	assert.Contains(t, got, "        ---\n        a: 1", "the script was disturbed")
	assert.Contains(t, got, "      params:\n        lang: bash", "the migration itself did not happen")
}

// Every example ships on the current major, so migration must be a no-op on
// them — byte for byte, or the examples are not what the engine actually reads.
func TestMigrateLeavesCurrentExamplesUntouched(t *testing.T) {
	for _, path := range exampleFiles(t) {
		t.Run(path, func(t *testing.T) {
			raw := readFile(t, path)
			out, changed, err := se.Migrate(raw)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, string(raw), string(out))
		})
	}
}
