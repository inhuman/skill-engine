package lint_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/inhuman/skill-engine/lint"
)

// wf builds a whole skill out of a workflow body.
func wf(body string) string { return head + "workflow:\n" + body }

// A working skill must produce nothing. A rule that fires on healthy skills is
// noise, and noise is what stops the real findings being read.
func TestCleanWorkflowIsSilent(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: search
      call: {tool: "docs:page_search", args: {query: "{{input}}"}, save_as: hits}
    - name: answer
      instruction: "answer from {{hits}}"
      tools: ["docs"]
`))
	for _, f := range rep.Findings {
		assert.Failf(t, "a false finding on a clean skill", "%s: %s", f.Rule, f.Message)
	}
}

// W1: a broken description must be a linter finding, not a crash at execution.
// The skill is prepared in advance — the mistake has to be visible when it is
// saved, not on the turn that needed it.
func TestW1_BrokenDescription(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: s
      switch: {var: x}
      instruction: both at once
`))
	f := requireFinding(t, rep, "W1", lint.SeverityError)
	assert.Contains(t, f.Message, "several actions")
}

// W1 stops the workflow rules: they read a description Validate has normalised,
// and running them on a half-parsed one reports about a skill nobody executes.
func TestW1_StopsTheOtherWorkflowRules(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: s
      switch: {var: x}
      instruction: both at once
    - name: loop
      for_each: {in: items, as: it, collect: never_written, steps: [{name: w, instruction: "{{it}}", tools: []}]}
`))
	requireFinding(t, rep, "W1", lint.SeverityError)
	requireQuiet(t, rep, "W6")
}

// W2: the program reaches for a server the skill never declared. At execution
// this would surface only on the branch execution happened to reach.
func TestW2_ServerNotDeclaredBySkill(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs", "tracker"]
  steps:
    - name: s
      instruction: do it
      tools: ["tracker"]
`))
	f := requireFinding(t, rep, "W2", lint.SeverityError)
	assert.Contains(t, f.Message, "tracker")
}

// A server that is declared but does not exist in the installation.
func TestW2_ServerNotRegistered(t *testing.T) {
	rep := lintSkill(t, `skill_engine_version: "2.1.0"
name: probe
description: d
servers: ["ghost"]
workflow:
  tools: ["ghost"]
  steps:
    - name: s
      instruction: do it
      tools: ["ghost"]
`)
	f := requireFinding(t, rep, "W2", lint.SeverityError)
	assert.Contains(t, f.Message, "not registered")
}

// W3: a typo in a call's tool name. Without the check it would ride into
// production on a branch that fires once a week.
func TestW3_UnknownToolOnServer(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: s
      call: {tool: "docs:page_serch", args: {query: x}, save_as: hits}
`))
	f := requireFinding(t, rep, "W3", lint.SeverityError)
	assert.Contains(t, f.Message, "page_serch")
	assert.Contains(t, f.Message, "page_search", "the finding lists what is actually there")
}

// W4: code substituted into the instruction's text goes through the model's
// context — and the model starts rewriting it instead of running it as written.
func TestW4_CodeAssetInPromptText(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  assets:
    probe:
      kind: code
      source: inline
      params: {lang: python}
      content: "print(1)"
  steps:
    - name: s
      instruction: "here is the code {{asset:probe}}"
      tools: []
`))
	f := requireFinding(t, rep, "W4", lint.SeverityWarn)
	assert.Contains(t, f.Message, "probe")
}

// Reference material passed only by reference never reaches the model — which
// is the whole reason it was written.
func TestW4_ReferenceAssetPassedByReference(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["runner"]
  assets:
    glossary:
      kind: text
      source: inline
      content: "PROD means production"
  steps:
    - name: s
      call:
        tool: "runner:exec"
        args: {stdin: {from: "asset:glossary"}}
        save_as: out
`))
	f := requireFinding(t, rep, "W4", lint.SeverityWarn)
	assert.Contains(t, f.Message, "glossary")
}

// W5 catches what a live run caught the hard way: the tool's name is right and
// a required argument is missing, so the server rejects the call at execution —
// after everything before it has been spent.
func TestW5_MissingRequiredArg(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: s
      call: {tool: "docs:page_get", args: {}, save_as: out}
`))
	f := requireFinding(t, rep, "W5", lint.SeverityError)
	assert.Contains(t, f.Message, "title")
}

// Complete arguments are silence — values come from variables and are not
// checked, only the presence of the keys.
func TestW5_CompleteArgsAreSilent(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: s
      call: {tool: "docs:page_get", args: {title: "{{input}}"}, save_as: out}
`))
	requireQuiet(t, rep, "W5")
}

// W6: a loop collecting into a variable nobody writes runs every iteration and
// gathers nothing, and the next step words its answer over an empty space.
func TestW6_CollectNobodyWrites(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: loop
      for_each:
        in: items
        as: it
        collect: located
        steps:
          - name: work
            instruction: "handle {{it}}"
            tools: []
            save_as: other
`))
	f := requireFinding(t, rep, "W6", lint.SeverityError)
	assert.Contains(t, f.Message, "located")
}

func TestW6_CollectWrittenIsSilent(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: loop
      for_each:
        in: items
        as: it
        collect: located
        steps:
          - name: work
            instruction: "handle {{it}}"
            tools: []
            save_as: located
`))
	requireQuiet(t, rep, "W6")
}

// W7: a built-in tool called by a step has to be declared — the same floor that
// `servers` is for MCP servers.
func TestW7_UndeclaredBuiltinInCall(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: draw
      call: {tool: "builtin:render_diagram", args: {name: chart}, save_as: chart}
`))
	f := requireFinding(t, rep, "W7", lint.SeverityError)
	assert.Contains(t, f.Message, "render_diagram")
	requireQuiet(t, rep, "W2") // builtin is not a server
}

// W8: a wrapped result substituted whole. What is dangerous is the absence of
// breakage — the loop makes one iteration over the envelope, and the counters
// honestly show zero, because there was nothing to count.
func TestW8_EnvelopeReachesMachineConsumers(t *testing.T) {
	rep := lintSkill(t, `skill_engine_version: "2.1.0"
name: probe
description: d
servers: ["runner"]
workflow:
  tools: ["runner"]
  steps:
    - name: split
      call: {tool: "runner:exec", args: {code: "print(1)"}, save_as: parts}
    - name: loop
      for_each:
        in: parts
        as: it
        collect: got
        steps:
          - name: work
            instruction: "handle {{it}}"
            tools: []
            save_as: got
    - name: render
      call: {tool: "runner:exec", args: {code: "print(2)", stdin: "{{parts}}"}, save_as: answer}
`)
	var loop, call *lint.Finding
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.Rule != "W8" {
			continue
		}
		switch {
		case strings.Contains(f.Message, "the loop iterates over"):
			loop = f
		case strings.Contains(f.Message, "the call passes"):
			call = f
		}
	}
	require.NotNilf(t, loop, "W8 missed the loop:\n%s", rep.Text())
	require.NotNilf(t, call, "W8 missed the call:\n%s", rep.Text())
	assert.Equal(t, lint.SeverityError, loop.Severity)
	assert.Equal(t, lint.SeverityError, call.Severity)
	assert.Contains(t, loop.Message, "parts.stdout", "the finding says which field to take")
	assert.Contains(t, loop.Message, "exit_code", "the envelope's shape is named, so the author recognises it")
}

// A model reads the envelope and digs the payload out: noise, not breakage —
// so a substitution into an instruction stays a warning.
func TestW8_InstructionIsOnlyAWarning(t *testing.T) {
	rep := lintSkill(t, `skill_engine_version: "2.1.0"
name: probe
description: d
servers: ["runner"]
workflow:
  tools: ["runner"]
  steps:
    - name: split
      call: {tool: "runner:exec", args: {code: "print(1)"}, save_as: parts}
    - name: tell
      instruction: "retell {{parts}}"
      tools: []
`)
	requireFinding(t, rep, "W8", lint.SeverityWarn)
}

// The field is taken explicitly — silence.
func TestW8_FieldTakenIsSilent(t *testing.T) {
	rep := lintSkill(t, `skill_engine_version: "2.1.0"
name: probe
description: d
servers: ["runner"]
workflow:
  tools: ["runner"]
  steps:
    - name: split
      call: {tool: "runner:exec", args: {code: "print(1)"}, save_as: parts}
    - name: loop
      for_each:
        in: parts.stdout
        as: it
        collect: got
        steps:
          - name: work
            instruction: "handle {{it}}"
            tools: []
            save_as: got
`)
	requireQuiet(t, rep, "W8")
}

// W9: a field that is declared and not required will go missing under load, and
// the whole chain stays quiet about it — the schema is satisfied, the step is
// ok, the answer parses.
func TestW9_ObjectWithoutRequired(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: judge
      instruction: judge it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [findings]
        properties:
          findings:
            type: array
            items:
              type: object
              properties:
                file: {type: string, maxLength: 200}
                message: {type: string, maxLength: 600}
`))
	f := requireFinding(t, rep, "W9", lint.SeverityError)
	assert.Contains(t, f.Message, "file, message")
}

func TestW9_RequiredPresentIsSilent(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: judge
      instruction: judge it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [findings]
        properties:
          findings:
            type: array
            items:
              type: object
              required: [file, message]
              properties:
                file: {type: string, maxLength: 200}
                message: {type: string, maxLength: 600}
`))
	requireQuiet(t, rep, "W9")
}

// Emptiness the flow examines itself is an answer with a branch, not silence.
// Demanding `required` there demands the impossible: a schema with one field
// the request may genuinely not contain cannot make it required (see W16).
func TestW9_EmptinessHandledByTheFlowIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: the release identifier
      model: small-model
      tools: []
      response_schema:
        type: object
        properties:
          id: {type: string}
      save_as: rel
    - if:
        cond: rel.id is empty
        then:
          - name: ask
            instruction: ask for the identifier
            tools: []
        else:
          - name: report
            instruction: "tell about {{rel.id}}"
            tools: []
`))
	requireQuiet(t, rep, "W9")
}

// The exemption covers the STEP's answer only: an entry inside an array has an
// emptiness of its own, and the `is empty` branch does not examine it.
func TestW9_NestedObjectStillNeedsRequired(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: gather the findings
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [findings]
        properties:
          findings:
            type: array
            items:
              type: object
              properties:
                file: {type: string, maxLength: 200}
      save_as: rel
    - if:
        cond: rel.findings is empty
        then:
          - name: nothing
            instruction: say there are none
            tools: []
`))
	requireFinding(t, rep, "W9", lint.SeverityError)
}

// W10: `from:` passes a payload by reference, so the template has to give a
// handle. A live miss cost a whole report: the step failed looking for a handle
// whose name began with the first bytes of the substituted text.
func TestW10_FromWantsAHandle(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["runner"]
  steps:
    - name: render
      call:
        tool: "runner:exec"
        args:
          code: "print(1)"
          stdin:
            - from: "{{tools_out}}"
        save_as: answer
`))
	f := requireFinding(t, rep, "W10", lint.SeverityError)
	assert.Contains(t, f.Message, ".mem")
}

func TestW10_HandleIsSilent(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["runner"]
  steps:
    - name: fetch
      call: {tool: "runner:exec", args: {code: "print(0)"}, save_as: tools_out}
    - name: render
      call:
        tool: "runner:exec"
        args:
          code: "print(1)"
          stdin:
            - from: "{{tools_out.mem}}"
        save_as: answer
`))
	requireQuiet(t, rep, "W10")
}

// W11: `params` is an open map, so a typo in a key does NOTHING, quietly. In
// the typed field of format 1.x this mistake was impossible — the openness was
// bought at the price of this check.
func TestW11_UnknownParamIsWarned(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  assets:
    script:
      kind: code
      source: inline
      params: {lnag: python}
      content: "print(1)"
  steps:
    - name: work
      instruction: do it
      tools: []
`))
	f := requireFinding(t, rep, "W11", lint.SeverityWarn)
	assert.Contains(t, f.Message, "lnag")
}

// The same typo means the language is not named at all: there is nothing to
// check the syntax with, and an error in the body waits for production.
func TestW11_CodeWithoutLang(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  assets:
    script: {kind: code, source: inline, content: "print(1)"}
  steps:
    - name: work
      instruction: do it
      tools: []
`))
	f := requireFinding(t, rep, "W11", lint.SeverityWarn)
	assert.Contains(t, f.Message, "syntax")
}

func TestW11_DeclaredLangIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  assets:
    script:
      kind: code
      source: inline
      params: {lang: python}
      content: "print(1)"
  steps:
    - name: work
      instruction: "run it"
      tools: []
`))
	requireQuiet(t, rep, "W11")
}

// W12: the step names a tool without saying how tools are called. The model
// reads the name as an available function and calls it directly.
func TestW12_ToolNameWithoutTheProtocol(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: search
      instruction: "find the page with page_search and return the title"
      tools: ["docs"]
`))
	f := requireFinding(t, rep, "W12", lint.SeverityWarn)
	assert.Contains(t, f.Message, "page_search")
	assert.Contains(t, f.Message, "call_tool", "the finding says how to call it properly")
}

func TestW12_ProtocolNamedIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: search
      instruction: "find it: call_tool(server=\"docs\", tool=\"page_search\", args={})"
      tools: ["docs"]
`))
	requireQuiet(t, rep, "W12")
}

// A step handed no tools cannot call a name: nothing breaks there, and there is
// nothing to make noise about.
func TestW12_StepWithoutToolsIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: tell
      instruction: "retell what page_search returned"
      tools: []
`))
	requireQuiet(t, rep, "W12")
}

// W13: a string field without a ceiling. The model is entitled to write up to
// the token limit, break off mid-string and take the whole document with it.
func TestW13_FreeTextInsideAnArray(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: judge
      instruction: judge it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [findings]
        properties:
          findings:
            type: array
            items:
              type: object
              required: [message]
              properties:
                message: {type: string}
`))
	f := requireFinding(t, rep, "W13", lint.SeverityWarn)
	assert.Contains(t, f.Message, "message")
}

// An array OF STRINGS has no `properties`, and the field check cannot see it.
func TestW13_ArrayOfStrings(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: judge
      instruction: judge it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [strengths]
        properties:
          strengths:
            type: array
            maxItems: 3
            items: {type: string}
`))
	f := requireFinding(t, rep, "W13", lint.SeverityWarn)
	assert.Contains(t, f.Message, "array of strings")
}

// A top-level slot field is left alone: a word is written there, it does not
// break off, and findings about it would drown the real ones.
func TestW13_SlotFieldIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: parse it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [env]
        properties:
          env: {type: string}
          mode: {type: string, enum: [a, b]}
`))
	requireQuiet(t, rep, "W13")
}

func TestW13_MaxLengthIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: judge
      instruction: judge it
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [summary]
        properties:
          summary: {type: string, maxLength: 600}
`))
	requireQuiet(t, rep, "W13")
}

// W15: a declared built-in that does not exist. The dispatcher drops the
// unknown name with a line in a log, the step runs without the tool, and the
// failure reads as the model's fault.
func TestW15_DeclaredBuiltinMustExist(t *testing.T) {
	rep := lintSkill(t, `skill_engine_version: "2.1.0"
name: probe
description: d
builtin_tools: ["draw_chart"]
workflow:
  steps:
    - name: work
      instruction: count it
      tools: []
`)
	f := requireFinding(t, rep, "W15", lint.SeverityError)
	assert.Contains(t, f.Message, "draw_chart")
	assert.Contains(t, f.Message, "run_script", "the finding lists what is available")
}

// W16: the schema demands the field and the instruction beside it allows the
// field to be empty. No output satisfies both, and the model picks one — either
// filler prose or whitespace up to the token ceiling.
func TestW16_RequiredFieldAllowedToBeEmpty(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: |-
        cluster — which cluster is named. Not named — staging.

        namespace — the namespace. Not named — an empty string.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [cluster, namespace]
        properties:
          cluster: {type: string}
          namespace: {type: string}
`))
	f := requireFinding(t, rep, "W16", lint.SeverityError)
	assert.Contains(t, f.Message, "`namespace`")
	assert.NotContains(t, f.Message, "`cluster`", "a default is a legal output — the rule stays quiet on it")
}

// The same contradiction stated in the schema's own field description.
func TestW16_EmptinessInTheFieldDescription(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: parse the request
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [namespace]
        properties:
          namespace:
            type: string
            description: the namespace; not named — an empty string
`))
	requireFinding(t, rep, "W16", lint.SeverityError)
}

// Skills are written in the language of whoever writes them. A rule that reads
// only one silently stops working for half a catalogue.
func TestW16_ReadsBothLanguages(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: |-
        namespace — имя namespace. Не назван — пустая строка.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [namespace]
        properties:
          namespace: {type: string}
`))
	requireFinding(t, rep, "W16", lint.SeverityError)
}

// A default instead of emptiness is no contradiction.
func TestW16_DefaultIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: |-
        env — the environment, when named explicitly. Not named — staging.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [env]
        properties:
          env: {type: string}
`))
	requireQuiet(t, rep, "W16")
}

// The emptiness belongs to a NEIGHBOURING field — the finding must not hang on
// the required one.
func TestW16_EmptinessOfANeighbourIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: |-
        key — the ticket key, LITERALLY from the request.

        hint — a clarification, if given. Not given — an empty string.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [key]
        properties:
          key: {type: string}
          hint: {type: string}
`))
	requireQuiet(t, rep, "W16")
}

// Fields are listed on ADJACENT lines as often as in paragraphs. Reading the
// description as far as the blank line lets the next field's "an empty string"
// hang the finding on this one — which is exactly what happened to a shipped
// example, where the field with a legitimate default was the one reported.
func TestW16_NeighbourOnTheNextLineIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: |
        cluster — which cluster is named; none named — staging.
        namespace — the namespace; none named — an empty string.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [cluster, namespace]
        properties:
          cluster: {type: string}
          namespace: {type: string}
`))
	f := requireFinding(t, rep, "W16", lint.SeverityError)
	assert.Contains(t, f.Message, "`namespace`")
	assert.NotContains(t, f.Message, "`cluster`", "the neighbour's emptiness was charged to a field with a default")
	assert.Len(t, findAll(rep, "W16"), 1, "one defect, one finding")
}

// "перезапустить" contains "пуст" and says nothing about emptiness. A
// substring search hung a false finding here on a live skill.
func TestW16_EmptinessInsideAnotherWordIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  tools: ["docs"]
  steps:
    - name: understand
      instruction: |-
        mode — «exec», если просят перезапустить сервис.
      model: small-model
      tools: []
      response_schema:
        type: object
        required: [mode]
        properties:
          mode: {type: string}
`))
	requireQuiet(t, rep, "W16")
}

// E4: a delegate step names a skill that is not in the catalogue. Composite
// skills are built out of these references, and a renamed target leaves the
// reference looking exactly as it did.
func TestE4_DelegateTargetMustExist(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: ask
      delegate: {skill: no-such-skill, task: "look it up", save_as: found}
`))
	f := requireFinding(t, rep, "E4", lint.SeverityWarn)
	assert.Contains(t, f.Message, "no-such-skill")
}

func TestE4_KnownDelegateIsQuiet(t *testing.T) {
	rep := lintSkill(t, wf(`  steps:
    - name: ask
      delegate: {skill: lookup, task: "look it up", save_as: found}
`))
	requireQuiet(t, rep, "E4")
}

// Rules must see inside every kind of nesting. A branch that fires once a week
// is exactly where a defect survives longest — and the walk is driven by the
// engine's own Branches(), so a new kind of branch is covered without an edit.
func TestRulesSeeInsideEveryNesting(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"switch", `  steps:
    - name: pick
      set: {var: mode, value: a}
    - name: branch
      switch:
        var: mode
        cases:
          a:
            - name: inner
              call: {tool: "docs:page_serch", args: {}, save_as: out}
`},
		{"if", `  steps:
    - name: pick
      set: {var: mode, value: a}
    - if:
        cond: mode == a
        then:
          - name: inner
            call: {tool: "docs:page_serch", args: {}, save_as: out}
`},
		{"for_each", `  steps:
    - name: pick
      set: {var: items, value: "a"}
    - name: loop
      for_each:
        in: items
        as: it
        steps:
          - name: inner
            call: {tool: "docs:page_serch", args: {}, save_as: out}
`},
		{"parallel", `  steps:
    - name: both
      parallel:
        branches:
          - - name: inner
              call: {tool: "docs:page_serch", args: {}, save_as: out}
          - - name: other
              set: {var: x, value: y}
`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep := lintSkill(t, wf("  tools: [\"docs\"]\n"+c.body))
			f := requireFinding(t, rep, "W3", lint.SeverityError)
			assert.Contains(t, f.Message, "page_serch")
		})
	}
}
