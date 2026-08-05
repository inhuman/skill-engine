package skillengine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A step can only NARROW the flow's tool set, and an empty flow set means the
// flow hands out nothing. Both halves of the engine have to agree on that, and
// they did not.
//
// `call` refused: allowServer answers "the flow declares no servers", with a
// comment saying in as many words that an empty set is not "everything is
// allowed". A model step in the same flow was handed exactly what it asked for.
// So `tools: []` on the flow plus `tools: [private]` on a step put `private` in
// front of the model — the guard the flow was written for, undone by the step
// it was written against.
func TestStepCannotWidenAnEmptyFlowSet(t *testing.T) {
	f := parseFlow(t, `
tools: []
steps:
  - name: think
    instruction: do it
    tools: ["private-server"]
`)
	r := &fakeRunner{answer: map[string]string{"think": "done"}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, r.seen, 1)

	assert.Empty(t, r.seen[0].Tools,
		"a step widened an empty flow set — the executor was handed a server the flow does not have")
}

// The two paths must answer the same question the same way: a `call` to that
// same server in that same flow is refused.
func TestBothPathsAgreeOnAnEmptyFlowSet(t *testing.T) {
	f := parseFlow(t, `
tools: []
steps:
  - name: fetch
    call: {tool: "private-server:get", save_as: out}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return "reached it", nil
		}),
	}, nil)
	require.Error(t, err, "the call path already refuses this — the model path must too")
	assert.Contains(t, err.Error(), "declares no servers")
}

// Narrowing a NON-empty set keeps working: that is what the field is for.
func TestStepNarrowsANonEmptyFlowSet(t *testing.T) {
	f := parseFlow(t, `
tools: ["a", "b"]
steps:
  - name: think
    instruction: do it
    tools: ["b", "c"]
`)
	r := &fakeRunner{answer: map[string]string{"think": "done"}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, r.seen[0].Tools, "the intersection is the step's radius")
}

// A step that names nothing inherits the flow's set, empty or not.
func TestStepWithoutToolsInheritsTheFlowSet(t *testing.T) {
	f := parseFlow(t, `
tools: ["a"]
steps:
  - name: think
    instruction: do it
`)
	r := &fakeRunner{answer: map[string]string{"think": "done"}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, r.seen[0].Tools)
}

// A missing executor is the embedder's configuration error, and it has to read
// like one. `call` and `delegate` already said so; a model step panicked on a
// nil pointer instead, taking the process with it.
func TestMissingRunnerIsAnErrorNotAPanic(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: think
    instruction: do it
    tools: []
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Deps.Runner",
		"the error must name the field the embedder forgot")
}

// And it goes through the step's policy like any other failure: a skill saying
// "carry on without it" is entitled to.
func TestMissingRunnerObeysOnError(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: think
    instruction: do it
    tools: []
    on_error: continue
    save_as: out
  - name: after
    set: {var: reached, value: "yes"}
`)
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.NoError(t, err, "on_error: continue must apply to a missing executor too")
	assert.Equal(t, "yes", vars["reached"], "the flow stopped instead of continuing")
	assert.Contains(t, vars["out"], "ERROR")
	assert.Equal(t, "error", outcome.Steps[0].Outcome)
}

// A parsed Flow is the natural thing to keep and run many times — one file, many
// turns. ExecuteWith validates on every call, and validation NORMALISES the
// description in place: it folds profiles into steps and moves a `save_as`
// written at step level into the call it belongs to.
//
// Two turns at once over one *Flow therefore write to the same structs from two
// goroutines. The result is the same either way — the passes are idempotent —
// but a data race is a data race, and the race detector is what an embedder
// will hear it from, in production, on the day the second request arrives.
func TestFlowCanBeRunConcurrently(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
profiles:
  small: {model: tiny, tools: []}
steps:
  - name: think
    profile: small
    instruction: "do it"
  - name: fetch
    call: {tool: "srv:get"}
    save_as: out
`)
	r := &fakeRunner{answer: map[string]string{"think": "done"}}
	caller := ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
		return "fetched", nil
	})

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Caller: caller}, nil)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoErrorf(t, err, "turn %d failed", i)
	}
}

// The other half of the same promise: validating a description must not change
// what the caller handed over. An embedder that keeps a Flow and inspects it —
// to render it, to diff it against the file on disk — must see the file, not
// the engine's working copy.
func TestValidateDoesNotMutateTheCallersFlow(t *testing.T) {
	f := parseFlow(t, `
profiles:
  small: {model: tiny}
steps:
  - name: think
    profile: small
    instruction: "do it"
`)
	require.NoError(t, f.Validate())
	assert.Empty(t, f.Steps[0].Run.Model,
		"Validate folded a profile into the caller's own Flow")
	assert.Equal(t, "small", f.Steps[0].Profile,
		"the description no longer says what the author wrote")
}

// A `save_as` written beside a call is normalised into the call. That must also
// happen without editing what the caller holds.
func TestValidateDoesNotMoveSaveAsInTheCallersFlow(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:get"}
    save_as: out
`)
	require.NoError(t, f.Validate())
	require.NotNil(t, f.Steps[0].Run, "normalisation emptied the caller's step")
	assert.Equal(t, "out", f.Steps[0].Run.SaveAs)
	assert.Empty(t, f.Steps[0].Call.SaveAs)
	_ = strings.TrimSpace
}
