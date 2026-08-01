package skillengine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// for_each over a list of lines — the observed case of "a list produced by a
// tool".
func TestForEachOverLines(t *testing.T) {
	c := &recordingCaller{out: "commit found"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: per_service
    for_each:
      in: services
      as: service
      collect: findings
      steps:
        - call:
            tool: srv:find_commit
            args: {service: "{{service}}"}
            save_as: findings
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c},
		map[string]string{"services": "billing\norders\n\nstate"})
	require.NoError(t, err)
	assert.Equal(t, 3, c.calls, "an empty line does not count as an item")
	assert.Equal(t, "commit found\n\ncommit found\n\ncommit found", vars["findings"])
}

// A JSON array (a step's structured result) is iterated element-wise.
func TestForEachOverJSONArray(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: items
      as: it
      collect: acc
      steps:
        - set: {var: acc, value: "item {{it}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"items": `["a","b"]`})
	require.NoError(t, err)
	assert.Equal(t, "item a\n\nitem b", vars["acc"])
}

// The ceiling is required in spirit: a loop over a collection of unknown length
// is a straight road to a runaway. Partial processing is SAID OUT LOUD rather
// than swallowed.
func TestForEachRespectsLimitAndSaysSo(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: items
      as: it
      collect: acc
      max_iterations: 2
      steps:
        - set: {var: acc, value: "{{it}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"items": "1\n2\n3\n4\n5"})
	require.NoError(t, err)
	assert.Contains(t, vars["acc"], "processed 2 of 5")
}

// The default ceiling applies even when the skill did not set one.
func TestForEachDefaultLimit(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: items
      as: it
      collect: acc
      steps:
        - set: {var: acc, value: "{{it}}"}
`)
	many := make([]string, 25)
	for i := range many {
		many[i] = "x"
	}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"items": strings.Join(many, "\n")})
	require.NoError(t, err)
	assert.Contains(t, vars["acc"], "processed 10 of 25", "the default ceiling")
}

func TestForEachEmptyCollection(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each: {in: items, as: it, steps: [{set: {var: a, value: "{{it}}"}}]}
  - set: {var: after, value: "the flow carried on"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "the flow carried on", vars["after"])
}

// A loop's on_error must WORK, not merely parse: the skill asks to mark the item
// and move on, and before this fix the loop instead died on the very first
// failure, losing the remaining iterations.
func TestForEachOnErrorContinue(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - name: loop
    for_each:
      in: items
      as: it
      collect: out
      on_error: continue
      max_iterations: 5
      steps:
        - name: work
          instruction: process it
          tools: []
          save_as: out
`), &f))

	// The second iteration fails: one failure must not take down the rest.
	r := &nthFailRunner{failAt: 2, text: "done"}

	vars, outcome, err := ExecuteWith(t.Context(), &f, Deps{Runner: r},
		map[string]string{"items": "a\nb\nc"})
	require.NoError(t, err, "the loop fell over on a failed iteration")
	assert.Equal(t, 3, r.calls, "the loop stopped instead of carrying on")

	var loop *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Kind == "for_each" {
			loop = &outcome.Steps[i]
		}
	}
	require.NotNil(t, loop, "the loop left no trace")
	assert.Equal(t, "degraded", loop.Outcome)
	assert.Contains(t, loop.Reason, "failed: 1")
	assert.NotEmpty(t, vars["out"])
}

// nthFailRunner fails on the Nth iteration and works on the rest.
type nthFailRunner struct {
	failAt, calls int
	text          string
}

func (r *nthFailRunner) Run(context.Context, StepRequest) (Result, error) {
	r.calls++
	if r.calls == r.failAt {
		return Result{}, errors.New("item not found")
	}
	return Result{Text: r.text}, nil
}

// A loop must walk the FULL collection, not a preview.
//
// A large `call:` result arrives in the variable truncated (4096 characters)
// with a working-memory handle. A loop over such a value would walk part of the
// list and return a result that looks complete — worse than an honest ceiling,
// because it stays silent. Found while porting a skill to reviewing diff chunks
// one at a time.
func TestForEachReadsFullCollectionFromMemory(t *testing.T) {
	full := "part-1\npart-2\npart-3\npart-4"
	r := &fakeRunner{answer: map[string]string{"look": "ok"}}
	f := parseFlow(t, `
steps:
  - for_each:
      in: parts
      as: p
      max_iterations: 10
      steps:
        - name: look
          instruction: "looking at {{p}}"
`)
	_, _, err := ExecuteWith(context.Background(), f,
		Deps{Runner: r, Memory: fakeMemory{"res-1": full}},
		map[string]string{"parts": "part-1\n[mem:res-1]"})
	require.NoError(t, err)
	require.Len(t, r.seen, 4, "walked all four parts, not the one from the preview")
}

// `in` is a variable name, not a template. Written as a template it stays
// silent: there is no variable named "{{parts}}", the loop makes zero iterations
// and reports "ok" — an empty walk is indistinguishable from a successful one.
// Refusing at parse time is cheaper.
func TestForEachInRejectsTemplate(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(`
steps:
  - for_each:
      in: "{{parts}}"
      as: p
      steps:
        - name: look
          instruction: "looking at {{p}}"
`), &f))
	err := f.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "variable NAME")
}

// `in` handles a field too, not only a whole variable: a tool's result is an
// envelope, and what must be walked is its contents rather than the envelope.
func TestForEachInAcceptsField(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"look": "ok"}}
	f := parseFlow(t, `
steps:
  - for_each:
      in: res.stdout
      as: p
      max_iterations: 10
      steps:
        - name: look
          instruction: "looking at {{p}}"
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r},
		map[string]string{"res": `{"exit_code":0,"stdout":"part-1\npart-2\npart-3"}`})
	require.NoError(t, err)
	require.Len(t, r.seen, 3, "walked the lines of stdout, not the whole envelope")
}
