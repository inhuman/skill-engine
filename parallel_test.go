package skillengine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingDelegate struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string
	err   error
}

func (d *recordingDelegate) Delegate(_ context.Context, skill, task string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, skill+": "+task)
	if d.err != nil {
		return "", d.err
	}
	return d.out[skill], nil
}

// delegate hands the work to another skill — what composite skills are made of.
func TestDelegateStep(t *testing.T) {
	d := &recordingDelegate{out: map[string]string{"ticket": "symptom: crashes on start"}}
	f := parseFlow(t, `
steps:
  - name: read_ticket
    delegate:
      skill: ticket
      task: "analyse ticket {{key}}"
      save_as: ticket
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Delegate: d}, map[string]string{"key": "PROJ-1"})
	require.NoError(t, err)
	assert.Equal(t, "symptom: crashes on start", vars["ticket"])
	assert.Equal(t, []string{"ticket: analyse ticket PROJ-1"}, d.calls, "task with substitution")
}

// Branches do NOT see each other's variables: otherwise the outcome would depend
// on who finished first, and the format exists for predictability.
func TestParallelBranchesAreIsolated(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: before, value: "shared"}
  - name: gather
    parallel:
      branches:
        - - set: {var: a, value: "first sees {{before}} but not {{b}}"}
        - - set: {var: b, value: "second"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "first sees shared but not ", vars["a"],
		"the sibling branch's variable was not substituted")
	assert.Equal(t, "second", vars["b"], "both branches' results came back into the flow")
}

// One branch failing under continue does not take the others down.
func TestParallelContinuesOnBranchFailure(t *testing.T) {
	d := &recordingDelegate{err: errors.New("source unavailable")}
	f := parseFlow(t, `
steps:
  - name: gather
    parallel:
      on_error: continue
      collect: evidence
      branches:
        - - set: {var: ok_branch, value: "found a clue"}
        - - name: broken
            delegate: {skill: fetch-logs, task: "logs", save_as: logs, on_error: continue}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Delegate: d}, nil)
	require.NoError(t, err, "the flow did not fall over because of one branch")
	assert.Equal(t, "found a clue", vars["ok_branch"])
	assert.Contains(t, vars["logs"], "ERROR", "the branch's failure was recorded, not lost")
	assert.Contains(t, vars["evidence"], "found a clue", "what was gathered got merged")
}

// Exiting the skill is a decision for the whole turn, not for one branch: the
// continue policy does not swallow it.
func TestParallelExitPropagates(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: gather
    parallel:
      on_error: continue
      branches:
        - - set: {var: a, value: "work"}
        - - exit: {reason: "not my case"}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExit, "the exit was not swallowed by the branch policy")
}

func TestParallelNeedsTwoBranches(t *testing.T) {
	_, _, err := ExecuteWith(context.Background(), parseFlow(t, `
steps:
  - parallel:
      branches:
        - - set: {var: a, value: "alone"}
`), Deps{}, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "fewer than two branches"), err.Error())
}
