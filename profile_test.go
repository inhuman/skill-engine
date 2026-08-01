package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A profile is inherited by every step that names it, and what a step spells
// out itself wins.
func TestProfileIsInheritedAndOverridden(t *testing.T) {
	f := parseFlow(t, `
tools: [srv]
profiles:
  strict:
    model: small/model
    sampling: {temperature: 0}
    tools: []
    max_calls: 3
steps:
  - name: classify
    profile: strict
    instruction: "classify {{input}}"
    save_as: kind
  - name: warmer
    profile: strict
    instruction: "word the answer"
    sampling: {temperature: 0.4}
`)
	r := &fakeRunner{answer: map[string]string{"classify": "a", "warmer": "b"}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, r.seen, 2)

	assert.Equal(t, "small/model", r.seen[0].Model, "the model did not come from the profile")
	require.NotNil(t, r.seen[0].Sampling)
	assert.Equal(t, float32(0), *r.seen[0].Sampling.Temperature)
	assert.Equal(t, 3, r.seen[0].MaxCalls)

	assert.Equal(t, "small/model", r.seen[1].Model, "an override of one field lost the rest of the profile")
	require.NotNil(t, r.seen[1].Sampling)
	assert.Equal(t, float32(0.4), *r.seen[1].Sampling.Temperature, "the step's own sampling did not win")
}

// The whole point of the empty set: `tools: []` in a profile is the guard "do
// not go to that source". If a profile could not express it, the guard would
// have to be repeated on every step by hand — which is what profiles exist to
// stop.
func TestProfileEmptyToolsIsAnEmptySet(t *testing.T) {
	f := parseFlow(t, `
tools: [srv, other]
profiles:
  sealed:
    tools: []
steps:
  - name: think
    profile: sealed
    instruction: "answer from what is here"
  - name: fetch
    instruction: "go and look"
`)
	r := &fakeRunner{}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, r.seen, 2)

	assert.Equal(t, []string{}, r.seen[0].Tools, "the profile's empty set became 'unset'")
	assert.Equal(t, []string{"srv", "other"}, r.seen[1].Tools, "a step without the profile lost the flow's set")
}

// Sampling is replaced whole rather than merged key by key: a half-inherited
// block turns "why is my top_k from the profile and my temperature my own" into
// a question at every debugging session.
func TestProfileSamplingIsReplacedWhole(t *testing.T) {
	f := parseFlow(t, `
profiles:
  tuned:
    sampling: {temperature: 0, top_k: 5}
steps:
  - name: s
    profile: tuned
    instruction: x
    tools: []
    sampling: {temperature: 0.7}
`)
	r := &fakeRunner{}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)

	s := r.seen[0].Sampling
	require.NotNil(t, s)
	assert.Equal(t, float32(0.7), *s.Temperature)
	assert.Nil(t, s.TopK, "top_k leaked in from the profile — the blocks were merged, not replaced")
}

// A profile supplying the model satisfies the response_schema pairing: the
// check runs after the profile is folded in, so a step that WILL have a model
// at execution time is not refused for lacking one in the source.
func TestProfileSatisfiesResponseSchemaPairing(t *testing.T) {
	f := parseFlow(t, `
profiles:
  strict:
    model: small/model
steps:
  - name: parse
    profile: strict
    instruction: pull the fields out
    tools: []
    response_schema: {type: object}
`)
	require.NoError(t, f.Validate())

	r := &fakeRunner{}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "small/model", r.seen[0].Model)
}

// ...and a profile that does NOT supply one leaves the hole open, so it is
// still refused. The rule is about the step as executed, not about where the
// model was written.
func TestProfileWithoutModelStillFailsResponseSchema(t *testing.T) {
	f := parseFlow(t, `
profiles:
  loose:
    sampling: {temperature: 0}
steps:
  - name: parse
    profile: loose
    instruction: pull the fields out
    tools: []
    response_schema: {type: object}
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response_schema without model")
}

// An unknown profile is an error, not an empty step. The engine already has one
// class of defect where an unknown name quietly resolves to nothing; a step
// silently stripped of its model and its radius still runs, and answers.
func TestUnknownProfileIsRejected(t *testing.T) {
	f := parseFlow(t, `
profiles:
  strict: {model: small/model}
steps:
  - name: s
    profile: stcirt
    instruction: x
    tools: []
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown profile "stcirt"`)
	assert.Contains(t, err.Error(), "s", "the failing step is not named")
}

// A profile carries generation parameters; on a step that generates nothing
// they have no effect. Accepting that silently is the "declared, validated and
// does nothing" class — the author would believe the step runs on the profile's
// model.
func TestProfileOnNonInstructionStepIsRejected(t *testing.T) {
	f := parseFlow(t, `
tools: [srv]
profiles:
  strict: {model: small/model}
steps:
  - name: fetch
    profile: strict
    call: {tool: "srv:get", save_as: out}
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an instruction step")
}

// Validate runs on every ExecuteWith, and embedders validate before storing a
// skill too. Folding a profile in twice must change nothing.
func TestProfileApplicationIsIdempotent(t *testing.T) {
	f := parseFlow(t, `
profiles:
  strict:
    model: small/model
    tools: []
steps:
  - name: s
    profile: strict
    instruction: x
`)
	require.NoError(t, f.Validate())
	require.NoError(t, f.Validate())
	require.NoError(t, f.Validate())

	r := &fakeRunner{}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "small/model", r.seen[0].Model)
	assert.Equal(t, []string{}, r.seen[0].Tools)
}

// Profiles reach steps nested in branches — a switch case is where half of a
// live catalogue's steps live.
func TestProfileReachesNestedSteps(t *testing.T) {
	f := parseFlow(t, `
profiles:
  strict:
    model: small/model
    tools: []
steps:
  - set: {var: kind, value: a}
  - switch:
      var: kind
      cases:
        a:
          - name: inner
            profile: strict
            instruction: x
`)
	r := &fakeRunner{}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Equal(t, "small/model", r.seen[0].Model, "a nested step did not inherit the profile")
	assert.Equal(t, []string{}, r.seen[0].Tools)
}

func TestProfileDeclarationIsValidated(t *testing.T) {
	for name, src := range map[string]string{
		"unknown on_error":     "profiles:\n  p: {on_error: retry}\nsteps: [{instruction: x, tools: []}]",
		"negative max_calls":   "profiles:\n  p: {max_calls: -1}\nsteps: [{instruction: x, tools: []}]",
		"negative tool errors": "profiles:\n  p: {max_tool_errors: -2}\nsteps: [{instruction: x, tools: []}]",
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, parseFlow(t, src).Validate())
		})
	}
}
