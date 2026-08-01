package skillengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emptyThenRunner answers with nothing until the Nth call, then with text.
type emptyThenRunner struct {
	textFrom int // the attempt from which text appears; 0 = never
	calls    int
	text     string
	err      error
}

func (r *emptyThenRunner) Run(context.Context, StepRequest) (Result, error) {
	r.calls++
	if r.err != nil {
		return Result{}, r.err
	}
	if r.textFrom > 0 && r.calls >= r.textFrom {
		return Result{Text: r.text}, nil
	}
	return Result{}, nil
}

// The default is what the engine did before the field existed: an instruction
// step is traced degraded and the flow moves on. A skill that never mentions
// on_empty must behave exactly as it did.
func TestEmptyDefaultKeepsOldBehaviour(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    save_as: verdict
  - set: {var: after, value: reached}
`)
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: &emptyThenRunner{}}, nil)
	require.NoError(t, err, "an empty step must not abort the flow by default")
	assert.Equal(t, "reached", vars["after"])
	assert.Equal(t, "degraded", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "produced no text")
}

// fail turns the emptiness into a failed step, and from there on_error decides
// — the default being abort. That is the live case: a judging step returned
// nothing, the rendering step ran ok on it, and the failure surfaced at an
// external gate a whole turn later.
func TestEmptyFailStopsTheFlow(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: fail
    save_as: verdict
  - name: render
    instruction: "render {{verdict}}"
    tools: []
`)
	r := &emptyThenRunner{}
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.Error(t, err, "the empty step was tolerated")
	assert.Contains(t, err.Error(), "empty result")
	assert.Contains(t, err.Error(), "judge", "the failing step is not named")
	assert.Equal(t, 1, r.calls, "the flow went on to the next step")
	assert.Equal(t, "degraded", outcome.Steps[0].Outcome)
}

// fail composes with on_error rather than replacing it: on_empty says what
// empty means, on_error says what to do with a step that failed.
func TestEmptyFailComposesWithOnError(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: fail
    on_error: continue
    save_as: verdict
  - set: {var: after, value: reached}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Runner: &emptyThenRunner{}}, nil)
	require.NoError(t, err)
	assert.Contains(t, vars["verdict"], "ERROR", "the failure was not recorded into the variable")
	assert.Equal(t, "reached", vars["after"], "the flow did not continue")
}

func TestEmptyRetry(t *testing.T) {
	t.Run("a retry that succeeds", func(t *testing.T) {
		f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: retry
    save_as: verdict
`)
		r := &emptyThenRunner{textFrom: 2, text: "found it"}
		vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
		require.NoError(t, err)
		assert.Equal(t, 2, r.calls, "the step was not run again")
		assert.Equal(t, "found it", vars["verdict"])
		assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	})

	t.Run("the count is honoured", func(t *testing.T) {
		f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: retry
    on_empty_retries: 3
    on_error: continue
    save_as: verdict
`)
		r := &emptyThenRunner{}
		_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
		require.NoError(t, err)
		assert.Equal(t, 4, r.calls, "one first attempt plus three retries")
	})

	// Spent retries and still empty means the author's expectation did not
	// hold: they asked to retry because empty was not acceptable, and carrying
	// on then is the very silence this exists to break.
	t.Run("exhausted retries fail", func(t *testing.T) {
		f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: retry
    save_as: verdict
`)
		_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: &emptyThenRunner{}}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty result")
	})

	t.Run("a failing retry is a failure, not another attempt", func(t *testing.T) {
		f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: retry
    on_empty_retries: 5
    save_as: verdict
`)
		r := &countingFailRunner{failFrom: 2, err: errors.New("upstream 500")}
		_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upstream 500")
		assert.Equal(t, 2, r.calls, "retrying continued past a hard failure")
	})
}

type countingFailRunner struct {
	failFrom, calls int
	err             error
}

func (r *countingFailRunner) Run(context.Context, StepRequest) (Result, error) {
	r.calls++
	if r.calls >= r.failFrom {
		return Result{}, r.err
	}
	return Result{}, nil
}

// use substitutes the declared value, and it goes through {{var}} substitution
// like any other value in the format.
func TestEmptyUseSubstitutesTheValue(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: judge
    instruction: judge it
    tools: []
    on_empty: use
    on_empty_value: "no findings for {{topic}}"
    save_as: verdict
  - name: render
    instruction: "render {{verdict}}"
    tools: []
`)
	r := &fakeRunner{answer: map[string]string{"render": "done"}}
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r},
		map[string]string{"topic": "the release"})
	require.NoError(t, err)
	assert.Equal(t, "no findings for the release", vars["verdict"])
	assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "on_empty", "the fallback firing is invisible in the trace")
	assert.Contains(t, r.seen[1].Instruction, "no findings for the release",
		"the next step did not receive the substituted value")
}

// The subtlety the engine has to get right: with `one_of` an ambiguous answer
// produces TEXT and stores NOTHING — normalizeOneOf refuses to guess. It is the
// stored value that flows on and sends a switch to its default, so that is what
// on_empty must judge.
func TestEmptyAppliesToTheStoredValueNotTheRawText(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: classify
    instruction: classify it
    tools: []
    one_of: [t1, foreign]
    on_empty: fail
    save_as: owner
`)
	// Both values named, no decision marker: the text is long, the value empty.
	r := &fakeRunner{answer: map[string]string{"classify": "could be t1, could be foreign"}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.Error(t, err, "a step that stored nothing was treated as if it had produced a value")
	assert.Contains(t, err.Error(), "empty result")
}

// Parity: a tool that returned nothing is the same class as a model that said
// nothing, so the mechanism exists on the call path too.
func TestEmptyOnCallStep(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		f := parseFlow(t, `
tools: [srv]
steps:
  - name: fetch
    call:
      tool: "srv:get"
      save_as: rows
      on_empty: fail
`)
		_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: &recordingCaller{out: "   "}}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty result")
	})

	t.Run("use", func(t *testing.T) {
		f := parseFlow(t, `
tools: [srv]
steps:
  - name: fetch
    call:
      tool: "srv:get"
      save_as: rows
      on_empty: use
      on_empty_value: "[]"
`)
		vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: &recordingCaller{out: ""}}, nil)
		require.NoError(t, err)
		assert.Equal(t, "[]", vars["rows"])
	})

	// The default stays what it was: a tool legitimately answers "nothing
	// found", and the engine cannot tell that from a broken one.
	t.Run("the default is unchanged", func(t *testing.T) {
		f := parseFlow(t, `
tools: [srv]
steps:
  - name: fetch
    call: {tool: "srv:get", save_as: rows}
`)
		_, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: &recordingCaller{out: ""}}, nil)
		require.NoError(t, err)
		assert.Equal(t, "ok", outcome.Steps[0].Outcome)
	})

	// Written at step level, where on_error and save_as also live for a model
	// step. Left in Run it would be a field the author believes in and the
	// engine never reads.
	t.Run("step-level on_empty reaches the call", func(t *testing.T) {
		f := parseFlow(t, `
tools: [srv]
steps:
  - name: fetch
    call: {tool: "srv:get"}
    save_as: rows
    on_empty: fail
`)
		_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: &recordingCaller{out: ""}}, nil)
		require.Error(t, err, "a step-level on_empty was silently ignored on a call step")
		assert.Contains(t, err.Error(), "empty result")
	})
}

// "A call step cannot be repeated" is a promise the format makes in as many
// words, and skills are written against it: a retried call with a side effect
// is a second ticket, a second merge request, a second e-mail.
func TestEmptyRetryRejectedOnCallStep(t *testing.T) {
	f := parseFlow(t, `
tools: [srv]
steps:
  - name: fetch
    call:
      tool: "srv:get"
      save_as: rows
      on_empty: retry
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a call cannot be repeated")
}

func TestEmptyPolicyValidation(t *testing.T) {
	for name, src := range map[string]string{
		"unknown value":             "steps: [{instruction: x, tools: [], on_empty: maybe}]",
		"use without value":         "steps: [{instruction: x, tools: [], on_empty: use}]",
		"value without use":         "steps: [{instruction: x, tools: [], on_empty: fail, on_empty_value: y}]",
		"value without any policy":  "steps: [{instruction: x, tools: [], on_empty_value: y}]",
		"retries without retry":     "steps: [{instruction: x, tools: [], on_empty: fail, on_empty_retries: 2}]",
		"retries above the ceiling": "steps: [{instruction: x, tools: [], on_empty: retry, on_empty_retries: 9}]",
		"negative retries":          "steps: [{instruction: x, tools: [], on_empty: retry, on_empty_retries: -1}]",
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, parseFlow(t, src).Validate())
		})
	}

	t.Run("a valid declaration passes", func(t *testing.T) {
		require.NoError(t, parseFlow(t, `
steps:
  - instruction: x
    tools: []
    on_empty: retry
    on_empty_retries: 2
`).Validate())
	})
}
