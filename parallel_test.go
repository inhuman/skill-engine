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

// delegate передаёт работу другому скиллу — то, чем заняты composite-скиллы.
func TestDelegateStep(t *testing.T) {
	d := &recordingDelegate{out: map[string]string{"jira-ticket": "симптом: падает на старте"}}
	f := parseFlow(t, `
steps:
  - name: read_ticket
    delegate:
      skill: jira-ticket
      task: "разбери тикет {{key}}"
      save_as: ticket
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Delegate: d}, map[string]string{"key": "PROJ-1"})
	require.NoError(t, err)
	assert.Equal(t, "симптом: падает на старте", vars["ticket"])
	assert.Equal(t, []string{"jira-ticket: разбери тикет PROJ-1"}, d.calls, "задача с подстановкой")
}

// Ветки НЕ видят переменных друг друга: иначе исход зависел бы от того, кто
// финишировал первым, а формат существует ради предсказуемости.
func TestParallelBranchesAreIsolated(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: before, value: "общее"}
  - name: gather
    parallel:
      branches:
        - - set: {var: a, value: "первая видит {{before}}, но не {{b}}"}
        - - set: {var: b, value: "вторая"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "первая видит общее, но не ", vars["a"],
		"переменная соседней ветки не подставилась")
	assert.Equal(t, "вторая", vars["b"], "результаты обеих веток вернулись в поток")
}

// Отказ одной ветки под continue не роняет остальные.
func TestParallelContinuesOnBranchFailure(t *testing.T) {
	d := &recordingDelegate{err: errors.New("источник недоступен")}
	f := parseFlow(t, `
steps:
  - name: gather
    parallel:
      on_error: continue
      collect: evidence
      branches:
        - - set: {var: ok_branch, value: "нашли улику"}
        - - name: broken
            delegate: {skill: k8s-logs, task: "логи", save_as: logs, on_error: continue}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Delegate: d}, nil)
	require.NoError(t, err, "поток не упал из-за одной ветки")
	assert.Equal(t, "нашли улику", vars["ok_branch"])
	assert.Contains(t, vars["logs"], "ERROR", "отказ ветки записан, а не потерян")
	assert.Contains(t, vars["evidence"], "нашли улику", "собранное сведено")
}

// Выход из скилла — решение всего хода, а не одной ветки: политика continue
// его не проглатывает.
func TestParallelExitPropagates(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: gather
    parallel:
      on_error: continue
      branches:
        - - set: {var: a, value: "работа"}
        - - exit: {reason: "не мой случай"}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExit, "выход не проглочен политикой ветки")
}

func TestParallelNeedsTwoBranches(t *testing.T) {
	_, _, err := ExecuteWith(context.Background(), parseFlow(t, `
steps:
  - parallel:
      branches:
        - - set: {var: a, value: "одна"}
`), Deps{}, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "двух"), err.Error())
}
