package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole table: which description runs is format semantics, and the version
// promises it. Silently shifting any row changes the behaviour of skills that
// are already written and that nobody will re-read.
func TestResolveMode(t *testing.T) {
	for _, c := range []struct {
		name                     string
		declared                 string
		hasWorkflow, hasPlaybook bool
		want                     Mode
		wantErr                  string
	}{
		{name: "no mode, both present", hasWorkflow: true, hasPlaybook: true, want: ModeWorkflow},
		{name: "no mode, steps only", hasWorkflow: true, want: ModeWorkflow},
		{name: "no mode, text only", hasPlaybook: true, want: ModePlaybook},
		{name: "no mode, nothing", wantErr: "describes no turn"},

		{name: "mode steps, both present", declared: "workflow", hasWorkflow: true, hasPlaybook: true, want: ModeWorkflow},
		{name: "mode steps, steps only", declared: "workflow", hasWorkflow: true, want: ModeWorkflow},
		{name: "mode steps, no steps", declared: "workflow", hasPlaybook: true, wantErr: "no steps"},

		{name: "mode text, both present", declared: "playbook", hasWorkflow: true, hasPlaybook: true, want: ModePlaybook},
		{name: "mode text, text only", declared: "playbook", hasPlaybook: true, want: ModePlaybook},
		{name: "mode text, no text", declared: "playbook", hasWorkflow: true, wantErr: "no text"},

		{name: "foreign value", declared: "flow", hasWorkflow: true, wantErr: "want workflow or playbook"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveMode(c.declared, c.hasWorkflow, c.hasPlaybook)
			if c.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), c.wantErr)
				assert.Empty(t, got, "no mode is handed out on refusal")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// An explicit mode with an empty half must refuse rather than fall back to the
// other one: a silent fallback gives a clean run over the description that was
// NOT selected, and conclusions about the selected one are drawn from it.
func TestResolveModeDoesNotFallBack(t *testing.T) {
	_, err := ResolveMode("playbook", true, false)
	require.Error(t, err, "falling back to steps would look like a successful turn")

	_, err = ResolveMode("workflow", false, true)
	require.Error(t, err, "falling back to text would look like a successful turn")
}
