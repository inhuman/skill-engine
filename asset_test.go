package skillengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAssets struct {
	content map[string]string
	err     error
	calls   int
	seen    []Asset
}

func (f *fakeAssets) Resolve(_ context.Context, name string, a Asset) (string, error) {
	f.calls++
	f.seen = append(f.seen, a)
	if f.err != nil {
		return "", f.err
	}
	return f.content[name], nil
}

// Code and config go into a tool ARGUMENT: a call: step is built by the skill,
// not by the model, so the payload never reaches the model at all — exactly what
// assets exist for (a model cannot reproduce a multi-kilobyte literal, it
// regenerates and corrupts it).
func TestAssetGoesIntoCallArgs(t *testing.T) {
	c := &recordingCaller{out: "done"}
	a := &fakeAssets{content: map[string]string{"chart": "import sys\nprint('ok')"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  chart:
    kind: code
    source: inline
    params: {lang: python}
    content: "does not matter — the resolver replaces it"
steps:
  - name: draw
    call:
      tool: exec:exec
      args:
        code: {from: "asset:chart"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	assert.Equal(t, "import sys\nprint('ok')", c.args["code"], "the content was substituted host-side")
}

// Kind-specific parameters reach the resolver as they were written: the engine
// does not know the application's kinds and passes params through untouched.
func TestAssetParamsReachResolver(t *testing.T) {
	c := &recordingCaller{out: "done"}
	a := &fakeAssets{content: map[string]string{"chart": "print(1)"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  chart:
    kind: code
    source: inline
    params: {lang: python, timeout_s: 30}
    content: "x"
steps:
  - name: draw
    call:
      tool: exec:exec
      args: {code: {from: "asset:chart"}}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Len(t, a.seen, 1)
	assert.Equal(t, "python", a.seen[0].Params["lang"])
	assert.Equal(t, 30, a.seen[0].Params["timeout_s"], "a non-string param keeps its type")
}

// Text and data are substituted INTO THE PROMPT: a lookup table is useless
// unless the model reads it.
func TestAssetSubstitutedIntoInstruction(t *testing.T) {
	r := &fakeRunner{}
	a := &fakeAssets{content: map[string]string{"metrics": "load: rate(http_requests_total[5m])"}}
	f := parseFlow(t, `
assets:
  metrics:
    kind: data
    source: inline
    content: "will be replaced"
steps:
  - name: parse
    instruction: |
      Reference:
      {{asset:metrics}}
      Query: {{input}}
    save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Assets: a},
		map[string]string{"input": "service load"})
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Contains(t, r.seen[0].Instruction, "rate(http_requests_total[5m])")
}

// One asset consumed by several steps is fetched ONCE: an external source on the
// hot path of a turn means network, and there is no reason to pay for it three
// times.
func TestAssetFetchedOncePerTurn(t *testing.T) {
	r := &fakeRunner{}
	a := &fakeAssets{content: map[string]string{"ref": "reference"}}
	f := parseFlow(t, `
assets:
  ref: {kind: data, source: inline, content: "x"}
steps:
  - {name: one, instruction: "{{asset:ref}}", save_as: a}
  - {name: two, instruction: "{{asset:ref}}", save_as: b}
  - {name: three, instruction: "{{asset:ref}}", save_as: c}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Assets: a}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, a.calls, "three consumers — one fetch")
}

// An unavailable asset expands to nothing rather than leaving a marker: a marker
// that reached the model would read as part of the instruction.
func TestUnavailableAssetExpandsToEmpty(t *testing.T) {
	r := &fakeRunner{}
	a := &fakeAssets{err: errors.New("source unavailable")}
	f := parseFlow(t, `
assets:
  ref: {kind: data, source: mcp, ref: "docs:get_page"}
steps:
  - {name: one, instruction: "before:{{asset:ref}}:after", save_as: a}
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r, Assets: a}, nil)
	require.NoError(t, err)
	assert.Equal(t, "before::after", r.seen[0].Instruction)
}

// Field pairings are checked BEFORE execution: an error in a declaration must be
// visible while writing the skill, not on a user's first run.
//
// Asset validation checks the SHAPE, not the vocabulary: the engine does not
// know what sources and payload kinds an application has, and rejecting an
// unfamiliar value would mean rejecting other people's. It knows one thing — the
// content is either here or at an address.
func TestAssetValidationChecksShapeNotVocabulary(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		for name, src := range map[string]string{
			"neither content nor ref":  `assets: {a: {kind: text}}`,
			"both content and ref":     `assets: {a: {content: "x", ref: "somewhere"}}`,
			"malformed on_unavailable": `assets: {a: {ref: "somewhere", fetch: {on_unavailable: somehow}}}`,
		} {
			t.Run(name, func(t *testing.T) {
				f := parseFlow(t, src+"\nsteps: [{set: {var: a, value: b}}]")
				require.Error(t, f.Validate())
			})
		}
	})

	t.Run("allowed", func(t *testing.T) {
		for name, src := range map[string]string{
			"unfamiliar source":   `assets: {a: {source: my-own-storage, ref: "address"}}`,
			"unfamiliar kind":     `assets: {a: {kind: blueprint, content: "x"}}`,
			"unfamiliar route":    `assets: {a: {content: "x", deliver: to-a-queue}}`,
			"code without params": `assets: {a: {kind: code, content: "x"}}`,
			"unfamiliar params":   `assets: {a: {kind: blueprint, params: {scale: "1:100"}, content: "x"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				f := parseFlow(t, src+"\nsteps: [{set: {var: a, value: b}}]")
				require.NoError(t, f.Validate(), "the engine does not judge the application's vocabulary")
			})
		}
	})
}

// The delivery route declared by an asset must reach the call's arguments: the
// bridge reads it from `_deliver`, and without carrying it over the declaration
// is decoration.
//
// Live case: a skill declared `deliver: reply` on a render asset, the step's
// output was not marked as the turn's answer, and the publication gate rejected
// the whole turn ("the skill's final step did not record a result").
func TestAssetDeliverReachesCallArgs(t *testing.T) {
	c := &recordingCaller{out: "## Review\nno remarks"}
	a := &fakeAssets{content: map[string]string{"render": "print('report')"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  render:
    kind: code
    source: inline
    params: {lang: python}
    deliver: reply
    content: "does not matter"
steps:
  - name: render
    call:
      tool: exec:exec
      args:
        code: {from: "asset:render"}
      save_as: answer
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"to": "reply"}, c.args["_deliver"],
		"the route declared by the asset is carried into the call's arguments")
}

// An explicit `_deliver` on the step wins over the asset's declaration: it is
// written for that specific call, while the declaration is a default for all of
// the asset's consumers.
func TestStepDeliverBeatsAssetDeliver(t *testing.T) {
	c := &recordingCaller{out: "data"}
	a := &fakeAssets{content: map[string]string{"probe": "print(1)"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  probe:
    kind: code
    source: inline
    params: {lang: python}
    deliver: reply
    content: "does not matter"
steps:
  - name: probe
    call:
      tool: exec:exec
      args:
        code: {from: "asset:probe"}
        _deliver: {to: memory}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"to": "memory"}, c.args["_deliver"], "the step overrides the asset")
}

// deliver: none is the author's deliberate refusal of delivery, not "forgot to
// set it".
func TestAssetDeliverNoneInjectsNothing(t *testing.T) {
	c := &recordingCaller{out: "data"}
	a := &fakeAssets{content: map[string]string{"calc": "print(2)"}}
	f := parseFlow(t, `
tools: ["exec"]
assets:
  calc:
    kind: code
    source: inline
    params: {lang: python}
    deliver: none
    content: "does not matter"
steps:
  - name: calc
    call:
      tool: exec:exec
      args:
        code: {from: "asset:calc"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.NotContains(t, c.args, "_deliver")
}

// An asset reference INSIDE A LIST: `command: ["sh","-c",{from: "asset:x"}]`.
// The shape is not exotic — it is how a script run in a k8s job is specified,
// and before that skill was ported to a program it was the only working one.
// During the port the sh -c wrapper was lost, the asset's content went into
// command as a STRING, and the tool rejected the call by schema (it expected an
// array) — and that only became visible once field substitution was fixed and
// the arguments stopped being empty.
func TestAssetRefInsideListIsResolved(t *testing.T) {
	c := &recordingCaller{out: "ok"}
	a := &fakeAssets{content: map[string]string{"battery": "echo hello"}}
	f := parseFlow(t, `
tools: ["k8s-job"]
assets:
  battery:
    kind: code
    source: inline
    params: {lang: bash}
    content: "does not matter"
steps:
  - name: run
    call:
      tool: k8s-job:run_job
      args:
        image: go-review
        command: ["sh", "-c", {from: "asset:battery"}]
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c, Assets: a}, nil)
	require.NoError(t, err)
	require.Equal(t, []any{"sh", "-c", "echo hello"}, c.args["command"],
		"a reference inside a list is replaced by content instead of staying an object")
}

// A working-memory handle in arguments is a REFERENCE ({from: "<id>"}), not a
// string. As a string the script receives an identifier instead of data and
// fails to parse it.
//
// Live case: a render step received stdin ["<json>", "mrctx-ab", "mrctx-ab"] and
// answered "RENDER_ERROR: stdin does not parse as a JSON stream"; there was
// nothing to deliver and the review was never published. The shape was lost
// while porting the skill to YAML — the same class as the lost sh -c wrapper.
func TestMemHandleRefStaysAReference(t *testing.T) {
	c := &recordingCaller{out: "ok"}
	f := parseFlow(t, `
tools: ["exec"]
steps:
  - name: render
    call:
      tool: exec:exec
      args:
        stdin: ["{{findings}}", {from: "{{ctx.mem}}"}]
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"findings": "{}", "ctx.mem": "res-7"})
	require.NoError(t, err)
	stdin, ok := c.args["stdin"].([]any)
	require.True(t, ok, "stdin stayed a list")
	require.Equal(t, map[string]any{"from": "res-7"}, stdin[1],
		"the handle went as a reference — the resolver substitutes the data; as a string the script would get an id")
}
