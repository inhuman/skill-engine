package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The turn's answer is written either by the model (an instruction step) or by
// a tool (a call step), and the caller must tell them apart: a model's draft may
// legitimately be rewritten "in voice", a deterministic report may not. A skill
// prints its report together with a metrics line from a python script, and
// having a model retell that text would destroy exactly the guarantees the
// render is deterministic for.
func TestOutcomeReportsWhatWroteTheAnswer(t *testing.T) {
	t.Run("the model answered", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: report
    instruction: answer
    tools: []
`), &f))
		_, out, err := ExecuteWith(t.Context(), &f,
			Deps{Runner: &fakeRunner{answer: map[string]string{"report": "done"}}}, nil)
		require.NoError(t, err)
		assert.Equal(t, "instruction", out.AnsweredBy)
	})

	t.Run("a tool answered", func(t *testing.T) {
		var f Flow
		require.NoError(t, yaml.Unmarshal([]byte(`
tools: [srv]
steps:
  - name: render
    call:
      tool: srv:render
      save_as: answer
`), &f))
		_, out, err := ExecuteWith(t.Context(), &f,
			Deps{Caller: &recordingCaller{out: "## report\n_metrics: published=10_"}}, nil)
		require.NoError(t, err)
		assert.Equal(t, "call", out.AnsweredBy)
	})
}
