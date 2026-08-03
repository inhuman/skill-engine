package skillengine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeRunner records what exactly the step executor was asked for and returns
// preset answers. Failures are keyed by step name.
type fakeRunner struct {
	seen   []StepRequest
	answer map[string]string
	fail   map[string]error
	// res — a ready-made result for tests that care about call counters rather
	// than about the text keyed by step name.
	res *Result
}

func (f *fakeRunner) Run(_ context.Context, req StepRequest) (Result, error) {
	f.seen = append(f.seen, req)
	if err, ok := f.fail[req.Name]; ok {
		return Result{}, err
	}
	if f.res != nil {
		return *f.res, nil
	}
	return Result{Text: f.answer[req.Name]}, nil
}

func run(t *testing.T, src string, r *fakeRunner) (map[string]string, error) {
	t.Helper()
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(src), &f))
	v, _, err := ExecuteWith(t.Context(), &f, Deps{Runner: r}, nil)
	return v, err
}

// The main thing the package exists for: the tool set is defined by the STEP,
// not by a request in the text. An empty list is not "unset" but "no tools at
// all".
func TestToolsPerStep(t *testing.T) {
	src := `
tools: ["search", "read", "tree"]
steps:
  - name: classify
    instruction: "determine the type"
    tools: []
    save_as: kind
  - name: lookup
    instruction: "find the schema"
    tools: ["search"]
    save_as: hit
  - name: answer
    instruction: "answer using {{hit}}"
`
	r := &fakeRunner{answer: map[string]string{"classify": "internal", "lookup": "a line from the repo"}}
	_, err := run(t, src, r)
	require.NoError(t, err)
	require.Len(t, r.seen, 3)

	assert.Equal(t, []string{}, r.seen[0].Tools, "the classification step must run WITHOUT tools")
	assert.Equal(t, []string{"search"}, r.seen[1].Tools, "the step narrowed the set to one")
	assert.Equal(t, []string{"search", "read", "tree"}, r.seen[2].Tools, "the step set none — the flow's set is used")
}

// A step can only NARROW the flow's set. Otherwise the flow's restriction means
// nothing: a step would hand itself back a tool that was removed on purpose.
func TestStepCannotWidenTools(t *testing.T) {
	src := `
tools: ["read"]
steps:
  - name: s
    instruction: "x"
    tools: ["read", "write", "delete"]
`
	r := &fakeRunner{}
	_, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, []string{"read"}, r.seen[0].Tools, "write/delete dropped — the flow does not have them")
}

func TestVarsAndBranching(t *testing.T) {
	src := `
steps:
  - name: detect
    instruction: "whose resource"
    save_as: owner
  - set:
      var: doc
      value: "docs/{{owner}}.md"
  - switch:
      var: owner
      cases:
        internal:
          - name: internal_path
            instruction: "read {{doc}}"
            save_as: out
        foreign:
          - name: foreign_path
            instruction: "answer from knowledge"
            save_as: out
      default:
        - name: unknown_path
          instruction: "ask again"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{"detect": "internal", "internal_path": "done"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "docs/internal.md", vars["doc"])
	assert.Equal(t, "done", vars["out"])
	require.Len(t, r.seen, 2)
	assert.Equal(t, "read docs/internal.md", r.seen[1].Instruction, "substitution reached the instruction")
}

func TestSwitchDefault(t *testing.T) {
	src := `
steps:
  - name: detect
    instruction: "?"
    save_as: kind
  - switch:
      var: kind
      cases:
        a:
          - name: branch_a
            instruction: "a"
      default:
        - name: fallback
          instruction: "could not tell"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{"detect": "something", "fallback": "asked again"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "asked again", vars["out"])
}

// A cascade of fallbacks: while the previous step gave no result, try the next
// way. That is how schema lookup works in live skills: search → file by path →
// tree.
func TestIfCascade(t *testing.T) {
	src := `
steps:
  - name: search
    instruction: "search"
    save_as: hit
  - if:
      cond: "hit == MISS"
      then:
        - name: by_path
          instruction: "by path"
          save_as: hit
  - if:
      cond: "hit == MISS"
      then:
        - name: by_tree
          instruction: "by tree"
          save_as: hit
`
	r := &fakeRunner{answer: map[string]string{"search": "MISS", "by_path": "MISS", "by_tree": "found it"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "found it", vars["hit"])
	assert.Len(t, r.seen, 3, "walked the whole cascade")
}

// An empty value in a condition tests for emptiness: "the key was not
// extracted".
func TestCondEmptyValue(t *testing.T) {
	src := `
steps:
  - name: extract
    instruction: "extract the key"
    save_as: key
  - if:
      cond: "key == "
      then:
        - name: ask
          instruction: "a key is needed"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{"extract": "", "ask": "asked"}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "asked", vars["out"])
}

// A permission refusal is the most common class of failure in live skills, and
// the reaction is always the same: say so honestly and continue with what is
// available. Retrying will not conjure permissions, and there are no workarounds
// worth looking for.
func TestOnErrorContinueOnDenied(t *testing.T) {
	src := `
steps:
  - name: read_attachment
    instruction: "read the attachment"
    save_as: att
    on_error: continue
  - name: answer
    instruction: "answer taking {{att}} into account"
    save_as: out
`
	r := &fakeRunner{
		fail:   map[string]error{"read_attachment": fmt.Errorf("%w: no permission for the tool", ErrDenied)},
		answer: map[string]string{"answer": "answered without the attachment"},
	}
	vars, err := run(t, src, r)
	require.NoError(t, err, "a permission refusal must not bring the flow down")
	assert.Contains(t, vars["att"], "DENIED")
	assert.Equal(t, "answered without the attachment", vars["out"])
	assert.Contains(t, r.seen[1].Instruction, "DENIED", "the next step SEES that there was no access")
}

func TestOnErrorAbortIsDefault(t *testing.T) {
	src := `
steps:
  - name: must_work
    instruction: "a required step"
  - name: never
    instruction: "must not run"
`
	r := &fakeRunner{fail: map[string]error{"must_work": errors.New("broke")}}
	_, err := run(t, src, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must_work")
	assert.Len(t, r.seen, 1, "the flow aborted, the second step never started")
}

func TestOnErrorSkipStopsBranchOnly(t *testing.T) {
	src := `
steps:
  - if:
      cond: "x == "
      then:
        - name: optional
          instruction: "optional"
          save_as: o
          on_error: skip
        - name: after_in_branch
          instruction: "inside the branch, after the failure"
  - name: after_branch
    instruction: "outside the branch"
    save_as: out
`
	r := &fakeRunner{
		fail:   map[string]error{"optional": errors.New("did not work out")},
		answer: map[string]string{"after_branch": "got here"},
	}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "got here", vars["out"], "the flow continued after the aborted branch")
	for _, s := range r.seen {
		assert.NotEqual(t, "after_in_branch", s.Name, "the rest of the branch was skipped")
	}
}

func TestLimitsReachRunner(t *testing.T) {
	src := `
steps:
  - name: flaky_search
    instruction: "search"
    max_calls: 1
  - name: tree_walk
    instruction: "tree"
    max_calls: 8
`
	r := &fakeRunner{}
	_, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, 1, r.seen[0].MaxCalls)
	assert.Equal(t, 8, r.seen[1].MaxCalls)
}

// An unknown variable turns into an empty string. A leftover {{var}} marker
// would reach the model and read to it as part of the instruction.
func TestUnknownVarBecomesEmpty(t *testing.T) {
	src := `
steps:
  - name: s
    instruction: "work with [{{nothing}}]"
`
	r := &fakeRunner{}
	_, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "work with []", r.seen[0].Instruction)
}

func TestValidate(t *testing.T) {
	bad := map[string]string{
		"empty flow":          `steps: []`,
		"step with no action": "steps:\n  - name: x",
		"switch without var":  "steps:\n  - switch:\n      cases: {}",
		"malformed condition": "steps:\n  - if:\n      cond: \"a > b\"\n      then: []",
		"unknown policy":      "steps:\n  - instruction: x\n    on_error: retry",
		"negative limit":      "steps:\n  - instruction: x\n    max_calls: -1",
	}
	for name, src := range bad {
		t.Run(name, func(t *testing.T) {
			var f Flow
			require.NoError(t, yaml.Unmarshal([]byte(src), &f))
			assert.Error(t, f.Validate())
		})
	}

	t.Run("a valid one passes", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte("steps:\n  - instruction: x\n    on_error: continue"), &f))
		assert.NoError(t, f.Validate())
	})
}

// A response_schema without a model is a silent hole: the decoding grammar does
// not survive every path to a model, and where it is dropped a "structured
// answer" degenerates into "the model usually answers JSON" — parsed by luck.
//
// The rule lived only in the JSON schema, which the engine cannot run (it would
// be a dependency), so an embedder without a schema validator never learned. It
// is duplicated in Validate on purpose: five of the nine examples in this
// repository broke it, and no Go test could see it.
func TestResponseSchemaRequiresModel(t *testing.T) {
	t.Run("rejected without a model", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: parse
    instruction: pull the fields out
    tools: []
    response_schema:
      type: object
      properties: {cluster: {type: string}}
`), &f))
		err := f.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "response_schema without model")
		assert.Contains(t, err.Error(), "parse", "the failing step is not named")
	})

	t.Run("accepted with a model", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: parse
    instruction: pull the fields out
    tools: []
    model: some/model
    response_schema:
      type: object
      properties: {cluster: {type: string}}
`), &f))
		assert.NoError(t, f.Validate())
	})

	// The schema attaches the rule to the STEP, not to the instruction: a stray
	// response_schema on a `call` step is the same hole, minus even the model
	// that could have honoured it.
	t.Run("rejected on a call step too", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
tools: [srv]
steps:
  - name: fetch
    call: {tool: "srv:get", save_as: out}
    response_schema:
      type: object
`), &f))
		require.Error(t, f.Validate())
	})

	// A model on its own stays optional — the pair is what matters.
	t.Run("a step without either is fine", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte("steps:\n  - instruction: answer\n    tools: []"), &f))
		assert.NoError(t, f.Validate())
	})
}

// The refusal must land BEFORE the first generation. Validate runs inside
// ExecuteWith, so a hole that used to be discovered by a mis-parsed answer
// halfway through the flow now costs nothing at all.
func TestResponseSchemaWithoutModelFailsBeforeAnyGeneration(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"parse": `{"cluster":"staging"}`}}
	_, _, err := ExecuteWith(t.Context(), parseFlow(t, `
steps:
  - name: parse
    instruction: pull the fields out
    tools: []
    response_schema: {type: object}
`), Deps{Runner: r}, nil)

	require.Error(t, err)
	assert.Empty(t, r.seen, "the flow reached the model before refusing")
}

// A classifier step exists for the sake of branching, while a model answers in
// prose. Live case: "answer in one word: t1 or foreign" got back "Summary:
// determined the resource type by prefix. Result: foreign" — correct in meaning,
// but a switch compares exactly and no branch was chosen.
func TestOneOfNormalizesVerboseAnswer(t *testing.T) {
	src := `
steps:
  - name: classify
    instruction: "determine the type"
    one_of: ["t1", "foreign"]
    save_as: owner
  - switch:
      var: owner
      cases:
        foreign:
          - name: public_answer
            instruction: "answer from knowledge"
            save_as: out
        t1:
          - name: internal_answer
            instruction: "check against the schema"
            save_as: out
      default:
        - name: ask
          instruction: "ask again"
          save_as: out
`
	r := &fakeRunner{answer: map[string]string{
		"classify":      "Summary: determined the resource type by prefix. Result: foreign",
		"public_answer": "answered",
	}}
	vars, err := run(t, src, r)
	require.NoError(t, err)
	assert.Equal(t, "foreign", vars["owner"], "the answer was reduced to an allowed value")
	assert.Equal(t, "answered", vars["out"], "the right branch was chosen")
}

func TestNormalizeOneOf(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	// Options enumerated before the choice: the LAST occurrence is taken.
	assert.Equal(t, "foreign",
		normalizeOneOf("we must decide between t1 and foreign; this one is foreign", allowed, markers))
	assert.Equal(t, "t1", normalizeOneOf("t1", allowed, markers))
	assert.Equal(t, "foreign", normalizeOneOf("FOREIGN", allowed, markers), "case does not matter")
	assert.Empty(t, normalizeOneOf("could not determine", allowed, markers), "nothing found — empty")
	assert.Equal(t, "as is", normalizeOneOf("as is", nil, markers), "without one_of the text is left alone")
}

// A step without save_as is the final answer, not discarded work: its text must
// reach the caller. It used to be stored nowhere, and the turn handed out the
// longest internal variable — the first step's parse instead of an answer.
func TestStepWithoutSaveAsFillsAnswer(t *testing.T) {
	out, err := run(t, `
steps:
  - name: parse
    instruction: parse it
    tools: []
    save_as: req
  - name: reply
    instruction: answer
    tools: []
`, &fakeRunner{answer: map[string]string{"parse": "a longer internal value", "reply": "here is the answer"}})
	require.NoError(t, err)
	assert.Equal(t, "here is the answer", out[AnswerVar])
}

// No branch matched and the default is empty — that is a failed branch, not
// "nothing to do": the step's work was not done. In the trace such a step must
// be marked as degraded, otherwise a silent failure looks like success.
func TestSwitchWithoutMatchAndEmptyDefaultIsLoud(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - set: {var: verdict, value: ""}
  - switch:
      var: verdict
      cases:
        found:
          - name: a
            instruction: x
            tools: []
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Runner: &fakeRunner{}}, nil)
	require.NoError(t, err)

	var sw *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "switch" {
			sw = &outcome.Steps[i]
		}
	}
	require.NotNil(t, sw, "the switch step is missing from the trace")
	assert.Equal(t, "degraded", sw.Outcome, "a silent branching failure")
	assert.Contains(t, sw.Reason, "no branch matched")
}

// A step that gave not a single word is a failure, not a success: its work was
// not done. The class is live — gpt-oss puts the answer into reasoning_content
// and leaves content empty.
func TestStepWithoutTextIsDegraded(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: mute
    instruction: answer
    tools: []
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Runner: &fakeRunner{}}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	assert.Equal(t, "degraded", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "produced no text")
}

// Delegation spawns a subagent — the most expensive operation of a turn. Without
// a trace it is invisible in both agent_events and the progress post: a human
// stares at "thinking…" while another skill works for a minute.
func TestDelegateIsTraced(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: ask_other
    delegate:
      skill: ticket
      task: analyse the ticket
      save_as: out
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("done")}, nil)
	require.NoError(t, err)

	require.Len(t, outcome.Steps, 1, "delegation left no trace")
	assert.Equal(t, "delegate", outcome.Steps[0].Kind)
	assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "ticket", "the trace does not show which skill got the work")
}

// A delegate's failure must be in the trace too: under the continue policy it is
// otherwise indistinguishable from success, while the work was not done.
func TestDelegateFailureIsTraced(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: ask_other
    delegate:
      skill: ticket
      task: analyse the ticket
      save_as: out
      on_error: continue
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: failingDelegate{}}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	assert.NotEqual(t, "ok", outcome.Steps[0].Outcome)
}

// A fork marks the boundary: how many branches went and how many failed.
// Otherwise a branch that failed under continue is indistinguishable from one
// that never started.
func TestParallelIsTraced(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: evidence
      on_error: continue
      branches:
        - - name: a
            delegate: {skill: one, task: t, save_as: x}
        - - name: b
            delegate: {skill: two, task: t, save_as: y}
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("done")}, nil)
	require.NoError(t, err)

	var par *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "parallel" {
			par = &outcome.Steps[i]
		}
	}
	require.NotNil(t, par, "the fork left no trace")
	assert.Contains(t, par.Reason, "branches: 2")
}

type constDelegate string

func (c constDelegate) Delegate(context.Context, string, string) (string, error) {
	return string(c), nil
}

type failingDelegate struct{}

func (failingDelegate) Delegate(context.Context, string, string) (string, error) {
	return "", errors.New("the subagent fell over")
}

// A fork where ALL branches were skipped by condition gathered nothing. The next
// step will word an answer anyway and it will look complete — so such a fork must
// be marked as degraded.
func TestParallelAllBranchesSkippedIsDegraded(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: evidence
      branches:
        - - name: a
            when: pick.wiki == true
            delegate: {skill: one, task: t, save_as: x}
        - - name: b
            when: pick.kg == true
            delegate: {skill: two, task: t, save_as: y}
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("data")},
		map[string]string{"pick": `{"wiki":false,"kg":false}`})
	require.NoError(t, err)

	var par *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "parallel" {
			par = &outcome.Steps[i]
		}
	}
	require.NotNil(t, par)
	assert.Equal(t, "degraded", par.Outcome, "all branches skipped, yet the fork counts as successful")
	assert.Contains(t, par.Reason, "none ran")
}

// At least one branch went — the fork is successful: a partial gather is a
// normal result, not a failure.
func TestParallelPartialSelectionIsOK(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: evidence
      branches:
        - - name: a
            when: pick.wiki == true
            delegate: {skill: one, task: t, save_as: x}
        - - name: b
            when: pick.kg == true
            delegate: {skill: two, task: t, save_as: y}
`), &f))

	_, outcome, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("data")},
		map[string]string{"pick": `{"wiki":true,"kg":false}`})
	require.NoError(t, err)

	for _, s := range outcome.Steps {
		if s.Kind == "parallel" {
			assert.Equal(t, "ok", s.Outcome, "a partial gather is not a failure")
		}
	}
}

// Skipped branches reach the step that words the answer. Without them it sees
// only what was gathered and reports "the source answered nothing" where the
// source was never visited — passing off the unchecked as checked.
func TestParallelExposesSkippedBranches(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: gather
    parallel:
      collect: findings
      branches:
        - - name: probe_wiki
            when: pick.wiki == true
            delegate: {skill: wiki-search, task: t, save_as: from_wiki}
        - - name: probe_tracker
            when: pick.tracker == true
            delegate: {skill: ticket, task: t, save_as: from_tracker}
`), &f))

	vars, _, err := ExecuteWith(t.Context(), &f, Deps{Delegate: constDelegate("wiki data")},
		map[string]string{"pick": `{"wiki":true,"tracker":false}`})
	require.NoError(t, err)

	assert.Contains(t, vars["findings"], "wiki data")
	assert.Contains(t, vars["findings"+SkippedSuffix], "probe_tracker",
		"the skipped branch is invisible to the next step")
	assert.NotContains(t, vars["findings"+SkippedSuffix], "probe_wiki")
}

// A step whose calls ALL failed must be degraded, not ok.
//
// Live case: a review step made 7 calls, the server rejected every one ("Either
// mergeRequestIid or branchName must be provided"), the step hit the ceiling and
// returned nothing — while the event recorded outcome=ok, calls=7. By the events
// the turn was indistinguishable from a successful one, and the digging had to
// happen in pod logs.
func TestAllCallsFailedIsDegraded(t *testing.T) {
	r := &fakeRunner{res: &Result{Text: "the model said something anyway", Calls: 7, CallsFailed: 7}}
	f := parseFlow(t, `
tools: ["repo"]
steps:
  - name: judge
    instruction: "review the MR"
    save_as: out
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	tr := outcome.Steps[0]
	assert.Equal(t, "degraded", tr.Outcome)
	assert.Equal(t, 7, tr.CallsFailed)
	assert.Contains(t, tr.Reason, "all tool calls failed")
}

// A partial failure is NOT a failed step: a useful result was obtained.
func TestSomeCallsFailedStaysOK(t *testing.T) {
	r := &fakeRunner{res: &Result{Text: "review ready", Calls: 5, CallsFailed: 2}}
	f := parseFlow(t, `
tools: ["repo"]
steps:
  - name: judge
    instruction: "review the MR"
    save_as: out
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	assert.Equal(t, 2, outcome.Steps[0].CallsFailed, "the failure count is visible on a successful step too")
}

// The miss budget from the skill must reach the executor. A field declared in
// the schema and not passed on is decoration: the skill writes it while the
// engine lives by its default, and the divergence stays silent (exactly how an
// asset's `deliver` lived until the first live failure).
func TestMaxToolErrorsReachesRunner(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"judge": "done"}}
	_, err := run(t, `
tools: ["repo"]
steps:
  - name: judge
    instruction: "review the MR"
    max_calls: 6
    max_tool_errors: 4
    save_as: out
`, r)
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Equal(t, 6, r.seen[0].MaxCalls)
	assert.Equal(t, 4, r.seen[0].MaxToolErrors)
}
