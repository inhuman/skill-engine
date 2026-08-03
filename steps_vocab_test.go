package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the built-in words must not become a silent failure of its own. A
// `one_of` step that produced prose and stored nothing, in an application that
// declared no markers, says so in the trace — otherwise the embedder sees a
// step that "sometimes decides" and the cause is a field they never filled in.
func TestMissingDecisionMarkersAreVisible(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: classify
    instruction: decide
    one_of: [t1, foreign]
    save_as: verdict
`)
	r := &fakeRunner{answer: map[string]string{"classify": "Result: t1, not foreign"}}

	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1)
	assert.Contains(t, outcome.Steps[0].Reason, "DecisionMarkers",
		"the empty result was reported without naming the one thing that could explain it")
}

// With the words supplied the same answer normalises, and nothing is reported.
func TestSuppliedDecisionMarkersDecide(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: classify
    instruction: decide
    one_of: [t1, foreign]
    save_as: verdict
`)
	r := &fakeRunner{answer: map[string]string{"classify": "Result: t1, not foreign"}}

	vars, outcome, err := ExecuteWith(context.Background(), f,
		Deps{Runner: r, Vocabulary: Vocabulary{DecisionMarkers: []string{"result:"}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "t1", vars["verdict"])
	assert.NotContains(t, outcome.Steps[0].Reason, "DecisionMarkers")
}
