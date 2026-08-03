package lint_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/inhuman/skill-engine/lint"
)

// W14: a typo in a variable's name. The engine resolves an unknown name to an
// EMPTY string in silence, and the failure reads as "the model did not
// understand the question" — while the mistake is one letter.
func TestW14_TypoInAVariableReference(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: find
      instruction: find it
      tools: []
      save_as: page
    - name: tell
      instruction: "retell {{pag}}"
      tools: []
`))
	f := requireFinding(t, rep, "W14", lint.SeverityError)
	assert.Contains(t, f.Message, "pag")
	assert.Contains(t, f.Message, "page", "the finding lists what is actually available")
}

// A step's name and a variable's name are different things. The live bug this
// rule found: a step called `detect_lang` wrote into `lang`, and the
// instruction below substituted `{{detect_lang}}` — emptiness in the very place
// the language was decided.
func TestW14_AStepNameIsNotAVariable(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: detect_lang
      instruction: detect the language
      tools: []
      save_as: lang
    - name: use
      instruction: "the conventions of {{detect_lang}}"
      tools: []
`))
	requireFinding(t, rep, "W14", lint.SeverityError)
}

// Order matters: a variable declared BELOW is empty in a step above. The name
// exists, a search through the file finds it, and suspicion falls on anything
// except the order of the steps.
func TestW14_ForwardReference(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: early
      instruction: "use {{later}}"
      tools: []
      save_as: first
    - name: late
      instruction: compute it
      tools: []
      save_as: later
`))
	requireFinding(t, rep, "W14", lint.SeverityError)
}

// Legitimate names stay quiet: the host's own variables, an object's field, the
// engine's suffixes, a loop's item inside its own loop, variables from branches.
func TestW14_LegitimateReferencesAreQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  vars:
    seed: "the seed"
  steps:
    - name: prep
      instruction: "on request {{input}}, seed {{seed}}"
      tools: []
      save_as: ctx
    - name: branch
      switch:
        var: ctx.mode
        cases:
          mr:
            - name: inner
              instruction: parse it
              tools: []
              save_as: parts
        default:
          - name: skip
            set: {var: parts, value: "none"}
    - name: loop
      for_each:
        in: parts
        as: item
        collect: results
        steps:
          - name: one
            instruction: "handle {{item}} in {{ctx.repo}}"
            tools: []
            save_as: one_out
    - name: final
      instruction: "gather {{results}}, memory {{ctx.mem}}, skips {{results.skipped}}"
      tools: []
`))
	requireQuiet(t, rep, "W14")
}

// A variable the embedding application injects is not a typo. Without the host
// saying which ones it puts in, every skill that reads the request would be
// reported — and a rule that fires on every skill is a rule people turn off.
func TestW14_HostVariablesAreKnown(t *testing.T) {
	src := wf(`  tools: ["wiki"]
  steps:
    - name: work
      instruction: "answer {{input}}"
      tools: []
`)
	requireQuiet(t, lintSkill(t, src), "W14")

	opts := testOptions()
	opts.HostVars = nil
	rep, err := lint.Lint([]byte(src), testFacts(), opts)
	assert.NoError(t, err)
	requireFinding(t, rep, "W14", lint.SeverityError)
}

// W17: `switch.var` takes a NAME. Given a template the engine looks for a
// variable spelled with the braces, finds nothing, and compares an empty string
// against every case — so every branch falls through to default and the branch
// that was the point of the switch never runs. Nothing fails.
func TestW17_SwitchVarTakesAName(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: parse
      instruction: parse it
      tools: []
      save_as: ctx
    - name: branch
      switch:
        var: "{{ctx}}"
        cases:
          mr:
            - name: inner
              instruction: handle it
              tools: []
`))
	f := requireFinding(t, rep, "W17", lint.SeverityError)
	assert.Contains(t, f.Message, "default")
	requireQuiet(t, rep, "W14") // one defect, one finding
}

func TestW17_BareNameIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: parse
      instruction: parse it
      tools: []
      save_as: ctx
    - name: branch
      switch:
        var: ctx
        cases:
          mr:
            - name: inner
              instruction: handle it
              tools: []
`))
	requireQuiet(t, rep, "W17")
}

// A bare name in a switch is still a name that has to exist.
func TestW14_UnknownSwitchVariable(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: branch
      switch:
        var: ghost
        cases:
          mr:
            - name: inner
              instruction: handle it
              tools: []
`))
	f := requireFinding(t, rep, "W14", lint.SeverityError)
	assert.Contains(t, f.Message, "ghost")
}

// A condition's right-hand side is a LITERAL, not a variable. Re-deriving the
// grammar here instead of asking the engine is what would report it as missing.
func TestW14_ConditionValueIsNotAVariable(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["wiki"]
  steps:
    - name: parse
      instruction: parse it
      tools: []
      save_as: mode
    - if:
        cond: mode == production
        then:
          - name: inner
            instruction: handle it
            tools: []
`))
	requireQuiet(t, rep, "W14")
}

// W18: a `contains` matches a word that STARTS with the alternative, so a
// longer alternative beside a shorter one is dead weight. It is worth saying
// out loud, because a dead entry is the author believing they covered a case
// the short root was already swallowing.
func TestW18_AlternativeCoveredByAShorterOne(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: pick
      when: "input contains заказ | заказы | десерт"
      set: {var: course, value: order}
`))
	f := requireFinding(t, rep, "W18", lint.SeverityWarn)
	assert.Contains(t, f.Message, "заказы")
	assert.Contains(t, f.Message, "заказ")
}

func TestW18_DuplicateAlternative(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - if:
        cond: "input contains чай | кофе | чай"
        then:
          - name: pick
            set: {var: course, value: drink}
`))
	f := requireFinding(t, rep, "W18", lint.SeverityWarn)
	assert.Contains(t, f.Message, "listed twice")
	assert.Len(t, findAll(rep, "W18"), 1, "one dead entry, one finding")
}

// Alternatives that genuinely cover different words stay quiet — a rule that
// fires on a working dictionary is a rule people turn off.
func TestW18_IndependentAlternativesAreQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: pick
      when: "input contains десерт | сладк | напит | чай | кофе | салат"
      set: {var: course, value: any}
`))
	requireQuiet(t, rep, "W18")
}

// A condition with nothing to look for can never fire. It is refused by the
// engine's own validation rather than reported as advice — and the linter must
// still surface it, through W1.
func TestContainsWithoutAlternativesIsRefused(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: pick
      when: "input contains"
      set: {var: course, value: any}
`))
	f := requireFinding(t, rep, "W1", lint.SeverityError)
	assert.Contains(t, f.Message, "without anything to look for")
}

// References hide in every corner of a step, not only in the instruction: an
// argument at any depth, a condition, a computed server, the value put in place
// of an empty result.
func TestW14_ReferencesHideEverywhere(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"a nested call argument", `  steps:
    - name: call_it
      call:
        tool: "wiki:page_get"
        args: {title: {nested: ["{{ghost}}"]}}
        save_as: out
`},
		{"a when condition", `  steps:
    - name: maybe
      when: "ghost == yes"
      instruction: do it
      tools: []
`},
		{"a computed server", `  steps:
    - name: call_it
      on_server: "{{ghost}}"
      call: {tool: "page_get", args: {title: x}, save_as: out}
`},
		{"the value used for an empty result", `  steps:
    - name: work
      instruction: do it
      tools: []
      on_empty: use
      on_empty_value: "{{ghost}}"
      save_as: out
`},
		{"a delegate's task", `  steps:
    - name: ask
      delegate: {skill: lookup, task: "look up {{ghost}}", save_as: out}
`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep := lintSkill(t, wf("  tools: [\"wiki\"]\n"+c.body))
			f := requireFinding(t, rep, "W14", lint.SeverityError)
			assert.Contains(t, f.Message, "ghost")
		})
	}
}
