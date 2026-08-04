package skillengine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A branch of `parallel` runs in a state forked from the flow's, and everything
// the flow was given has to survive the fork.
//
// It did not. The sub-state was assembled by listing fields, and six of them
// were missing — assets, their resolver, their cache and context, working
// memory, and the application's vocabulary. Nothing failed loudly: an unknown
// asset expands to an empty string by contract, so `{{asset:x}}` inside a
// branch quietly became "", and the tool call that needed it lost a required
// argument. The error pointed at the argument, not at the substitution.
//
// It stayed hidden because no skill in a live catalogue of 29 had an asset
// inside a parallel branch — the path simply never ran.
func TestParallelBranchKeepsEverythingTheFlowHas(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
assets:
  payload:
    kind: code
    source: inline
    content: "print('the asset content')"
steps:
  - name: fork
    parallel:
      branches:
        - - name: uses_asset
            call:
              tool: "srv:run"
              args: {code: "{{asset:payload}}"}
              save_as: from_asset
        - - name: uses_memory
            instruction: "the whole of it: {{big}}"
            tools: []
            save_as: from_memory
`)

	var got map[string]any
	caller := ToolCallerFunc(func(_ context.Context, _, _ string, args map[string]any) (string, error) {
		got = args
		return "ran", nil
	})
	r := &fakeRunner{answer: map[string]string{"uses_memory": "read it"}}

	_, _, err := ExecuteWith(context.Background(), f, Deps{
		Runner: r,
		Caller: caller,
		Assets: assetFunc(func(_ context.Context, _ string, a Asset) (string, error) {
			return a.Content, nil
		}),
		Memory: fakeMemory{"res-1": "THE WHOLE VALUE"},
	}, map[string]string{"big": "preview…\n[mem:res-1]"})
	require.NoError(t, err)

	assert.Equal(t, "print('the asset content')", got["code"],
		"the asset resolved to an empty string inside the branch, and the call lost a required argument")
	assert.Contains(t, r.seen[0].Instruction, "THE WHOLE VALUE",
		"working memory did not reach the branch, so the step saw a fragment")
}

// The vocabulary is the application's words, and a branch that does not have
// them normalises an answer differently from the same step outside a branch.
func TestParallelBranchKeepsTheVocabulary(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: fork
    parallel:
      branches:
        - - name: classify
            instruction: decide
            one_of: [t1, foreign]
            save_as: verdict
        - - name: other
            set: {var: x, value: "y"}
`)
	r := &fakeRunner{answer: map[string]string{"classify": "Result: t1, not foreign"}}

	vars, _, err := ExecuteWith(context.Background(), f, Deps{
		Runner:     r,
		Vocabulary: Vocabulary{DecisionMarkers: []string{"result:"}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "t1", vars["verdict"],
		"the decision markers did not reach the branch, so a tie stayed unresolved")
}

// An asset needed by several branches is fetched ONCE: the cache is shared
// across the fork rather than cloned into it. Without a lock that sharing is a
// data race, which is why the cache has one.
func TestParallelBranchesShareTheAssetCache(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
assets:
  payload: {kind: code, source: inline, content: "shared"}
steps:
  - name: fork
    parallel:
      branches:
        - - name: a
            call: {tool: "srv:run", args: {code: "{{asset:payload}}"}, save_as: ra}
        - - name: b
            call: {tool: "srv:run", args: {code: "{{asset:payload}}"}, save_as: rb}
        - - name: c
            call: {tool: "srv:run", args: {code: "{{asset:payload}}"}, save_as: rc}
`)
	var mu sync.Mutex
	fetches := 0
	_, _, err := ExecuteWith(context.Background(), f, Deps{
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return "ok", nil
		}),
		Assets: assetFunc(func(_ context.Context, _ string, a Asset) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			fetches++
			return a.Content, nil
		}),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, fetches, "the same asset was fetched once per branch")
}

// What a branch must NOT inherit: the flow's collected traces and skips. A
// branch reporting the steps that ran before the fork would double-count them.
func TestParallelBranchStartsWithACleanTrace(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: before
    when: "missing == yes"
    set: {var: a, value: "1"}
  - name: fork
    parallel:
      branches:
        - - name: x
            set: {var: b, value: "2"}
        - - name: y
            when: "missing == yes"
            set: {var: c, value: "3"}
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"before", "y"}, outcome.Skipped,
		"a branch inherited the flow's skip list and reported it again")
	assert.Equal(t, 1, strings.Count(strings.Join(outcome.Skipped, " "), "before"))
}

// assetFunc — a resolver as a function, so a test does not need a type.
type assetFunc func(ctx context.Context, name string, a Asset) (string, error)

func (f assetFunc) Resolve(ctx context.Context, name string, a Asset) (string, error) {
	return f(ctx, name, a)
}

// The one asymmetry in Outcome, pinned so it stays a documented property rather
// than an accident: Skipped includes what a `parallel` branch skipped, Steps
// does not include what a branch ran.
//
// Nothing is lost by it — branch steps reach Deps.OnStep as they happen, which
// is where per-step telemetry comes from. But the two fields disagree, and a
// reader who checks one and assumes the other is the next person to lose an
// afternoon. If this test starts failing because branch traces were merged in,
// that is a behaviour change for every embedder reading Outcome.Steps: say so
// in the CHANGELOG and update the doc comment on Outcome.
func TestOutcomeReportsBranchesOnlyThroughSkippedAndOnStep(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: fork
    parallel:
      branches:
        - - name: ran_in_branch
            set: {var: a, value: "1"}
        - - name: skipped_in_branch
            when: "missing == yes"
            set: {var: b, value: "2"}
`)
	// The lock is not test hygiene — it is the contract. OnStep fires from the
	// goroutine that ran the step, so inside a `parallel` it fires from several
	// at once, and a callback appending to a slice without one is a data race
	// in the embedder. Written here the way an embedder has to write it.
	var mu sync.Mutex
	var live []string
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{
		OnStep: func(tr StepTrace) {
			mu.Lock()
			defer mu.Unlock()
			live = append(live, tr.Name)
		},
	}, nil)
	require.NoError(t, err)

	var inOutcome []string
	for _, s := range outcome.Steps {
		inOutcome = append(inOutcome, s.Name)
	}
	assert.Equal(t, []string{"fork"}, inOutcome,
		"Outcome.Steps gained the branch steps — a change for everyone reading it")
	assert.Contains(t, outcome.Skipped, "skipped_in_branch",
		"Outcome.Skipped must include what a branch skipped")
	assert.Subset(t, live, []string{"ran_in_branch", "skipped_in_branch", "fork"},
		"OnStep is where branch steps are visible, and they were not")
}
