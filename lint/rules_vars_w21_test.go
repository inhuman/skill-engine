package lint_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/inhuman/skill-engine/lint"
)

// W21: the case the rule was written for, taken from a live catalogue. The step
// saving `pick` declares only `index`, a bail-out branch is keyed on
// `pick.name`, and it had never run once — while its author believed the
// behaviour was in effect and had committed it as the important one.
func TestW21_FieldMissingFromDeclaredSchema(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: choose
      instruction: pick one
      tools: []
      save_as: pick
      model: test-model
      response_schema:
        type: object
        properties:
          index: {type: integer}
        required: [index]
    - name: bail
      if:
        cond: pick.name == NONE
        then:
          - name: stop
            exit: {reason: not found}
    - name: tell
      instruction: "номер {{pick.index}}"
      tools: []
`))
	f := requireFinding(t, rep, "W21", lint.SeverityError)
	assert.Contains(t, f.Message, "name", "the finding names the field that does not exist")
	assert.Contains(t, f.Message, "index", "and what the schema does declare, so the difference is visible")
}

// Declared fields and the engine's own suffixes are silent.
func TestW21_DeclaredFieldAndEngineSuffixAreFine(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: choose
      instruction: pick one
      tools: []
      save_as: pick
      model: test-model
      response_schema:
        type: object
        properties:
          index: {type: integer}
        required: [index]
    - name: tell
      instruction: "номер {{pick.index}}, данные {{pick.mem}}"
      tools: []
`))
	assert.Nil(t, find(rep, "W21"), "report:\n%s", rep.Text())
}

// A tool's result has no shape the linter can know. Guessing one would produce
// findings about fields that are really there — the fastest way to teach an
// author to ignore the report.
func TestW21_SaysNothingAboutShapesItCannotKnow(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: fetch
      call: {tool: "docs:page_get", args: {}, save_as: page}
    - name: tell
      instruction: "имя {{page.metadata.name}}"
      tools: []
`))
	assert.Nil(t, find(rep, "W21"), "report:\n%s", rep.Text())
}
