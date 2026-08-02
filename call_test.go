package skillengine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type recordingCaller struct {
	server, tool string
	args         map[string]any
	out          string
	err          error
	calls        int
}

func (c *recordingCaller) CallTool(_ context.Context, server, tool string, args map[string]any) (string, error) {
	c.calls++
	c.server, c.tool, c.args = server, tool, args
	return c.out, c.err
}

// seqCaller returns prepared answers in order and remembers the arguments.
type seqCaller struct {
	outs []string
	seen []map[string]any
}

func (c *seqCaller) CallTool(_ context.Context, _, _ string, args map[string]any) (string, error) {
	c.seen = append(c.seen, args)
	if len(c.seen) <= len(c.outs) {
		return c.outs[len(c.seen)-1], nil
	}
	return "", nil
}

func parseFlow(t *testing.T, src string) *Flow {
	t.Helper()
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(src), &f))
	return &f
}

// A call step spends no generations at all: there is no step executor here.
func TestCallStepNeedsNoModel(t *testing.T) {
	c := &recordingCaller{out: "schema lines"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: lookup
    call:
      tool: srv:search_code
      args:
        project_id: "iac/provider"
        search: "{{resource}}"
      save_as: schema
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"resource": "t1_vpc_vip"})
	require.NoError(t, err)
	assert.Equal(t, "schema lines", vars["schema"])
	assert.Equal(t, 1, c.calls)
	assert.Equal(t, "srv", c.server)
	assert.Equal(t, "search_code", c.tool)
	assert.Equal(t, "t1_vpc_vip", c.args["search"], "{{var}} was substituted")
	assert.Equal(t, "iac/provider", c.args["project_id"], "the literal was left alone")
}

// The main property: a skill cannot go around its own narrowing of servers.
func TestCallStepCannotEscapeFlowTools(t *testing.T) {
	c := &recordingCaller{out: "must not happen"}
	f := parseFlow(t, `
tools: ["srv-dev"]
steps:
  - name: sneaky
    call:
      tool: srv-prod:get_project
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "srv-prod")
	assert.Zero(t, c.calls, "it never got as far as the call")
}

// Arguments may be nested; substitution applies to strings only, the call's
// shape and types stay as written.
func TestCallStepExpandsNestedArgsOnly(t *testing.T) {
	c := &recordingCaller{}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - call:
      tool: srv:tool
      args:
        recursive: true
        depth: 3
        filter: {path: "docs/{{name}}.md"}
        tags: ["{{name}}", "static"]
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"name": "vpc_vip"})
	require.NoError(t, err)
	assert.Equal(t, true, c.args["recursive"])
	assert.Equal(t, 3, c.args["depth"])
	assert.Equal(t, "docs/vpc_vip.md", c.args["filter"].(map[string]any)["path"])
	assert.Equal(t, []any{"vpc_vip", "static"}, c.args["tags"])
}

func TestCallStepErrorPolicy(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: flaky
    call:
      tool: srv:search
      save_as: schema
      on_error: continue
  - name: after
    call:
      tool: srv:other
      save_as: second
      on_error: continue
`)
	c := &recordingCaller{err: errors.New("upstream 500")}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err, "continue does not bring the flow down")
	assert.Contains(t, vars["schema"], "ERROR")
	assert.Equal(t, 2, c.calls, "the flow reached the next step")

	c = &recordingCaller{err: ErrDenied}
	vars, _, _ = ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	assert.Contains(t, vars["schema"], "DENIED", "a permission refusal is distinguishable from a breakage")
}

// Without a call executor the step must refuse clearly rather than silently skip.
func TestCallStepWithoutCaller(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: lookup
    call:
      tool: srv:tool
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lookup")
}

func TestCallStepValidation(t *testing.T) {
	_, _, err := ExecuteWith(context.Background(), parseFlow(t, "tools: [\"srv\"]\nsteps:\n  - call:\n      tool: no_colon_here\n"), Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `want "server:tool"`)

	server, tool, ok := SplitToolRef("srv:ns:tool")
	assert.True(t, ok)
	assert.Equal(t, "srv", server)
	assert.Equal(t, "ns:tool", tool, "the separator is the first colon")
}

// "The step produced no result" must cover both emptiness and a marked failure:
// to a "nothing found, try another way" branch they mean the same thing.
func TestIsEmptyCondition(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "schema is empty"
      then:
        - set: {var: took_fallback, value: "yes"}
`)
	for name, v := range map[string]string{
		"empty":              "",
		"whitespace":         "   ",
		"breakage":           "ERROR: upstream 500",
		"permission refusal": "DENIED: skill-engine: denied",
	} {
		t.Run(name, func(t *testing.T) {
			vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": v})
			require.NoError(t, err)
			assert.Equal(t, "yes", vars["took_fallback"], "the fallback path was taken")
		})
	}

	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": "schema lines"})
	require.NoError(t, err)
	assert.Empty(t, vars["took_fallback"], "there is a result — no fallback needed")
}

func TestIsNotEmptyCondition(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "schema is not empty"
      then:
        - set: {var: used, value: "yes"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": "lines"})
	require.NoError(t, err)
	assert.Equal(t, "yes", vars["used"])

	vars, _, err = ExecuteWith(context.Background(), f, Deps{}, map[string]string{"schema": "ERROR: nope"})
	require.NoError(t, err)
	assert.Empty(t, vars["used"])
}

// A flow returns its RESULT, not what it was given as input. Otherwise a caller
// looking for "the most substantial variable" finds the conversation history it
// passed in.
func TestExecuteReturnsOnlyProduced(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: answer, value: "done"}
`)
	in := map[string]string{
		"input":   "the user's question",
		"history": "a very, very long conversation history, longer than any answer",
	}
	got, _, err := ExecuteWith(context.Background(), f, Deps{}, in)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"answer": "done"}, got,
		"input variables are not the flow's result")
}

// A step that overwrote an input variable PRODUCED it — it is a result.
func TestExecuteStepOverwriteCountsAsProduced(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: input, value: "rewritten by the step"}
`)
	got, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"input": "original"})
	require.NoError(t, err)
	assert.Equal(t, "rewritten by the step", got["input"])
}

// A flow without a single server does NOT allow everything: otherwise a skill
// with no `servers` (the field is optional) would reach write tools.
func TestCallStepDeniedWhenFlowHasNoServers(t *testing.T) {
	c := &recordingCaller{out: "must not happen"}
	f := parseFlow(t, `
steps:
  - name: sneaky
    call:
      tool: gitlab-write-prod:create_merge_request
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no servers")
	assert.Zero(t, c.calls, "it never got as far as the call")
}

// `when` is a step's applicability condition: false → the step does not run at
// all.
func TestWhenSkipsStep(t *testing.T) {
	c := &recordingCaller{out: "must not happen"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: only_if_found
    when: "found == yes"
    call:
      tool: srv:tool
      save_as: out
  - set: {var: done, value: "the end"}
`)
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"found": "no"})
	require.NoError(t, err)
	assert.Zero(t, c.calls, "the step did not run")
	assert.Equal(t, "the end", vars["done"], "the flow moved on")
	assert.Equal(t, []string{"only_if_found"}, outcome.Skipped,
		"the skip is VISIBLE: otherwise a partial match of the task goes unnoticed again")
}

func TestWhenRunsStepWhenTrue(t *testing.T) {
	c := &recordingCaller{out: "data"}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: guarded
    when: "found is not empty"
    call:
      tool: srv:tool
      save_as: out
`)
	vars, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"found": "yes"})
	require.NoError(t, err)
	assert.Equal(t, 1, c.calls)
	assert.Equal(t, "data", vars["out"])
	assert.Empty(t, outcome.Skipped)
}

// An exit step stops the flow with a special error: the caller must tell it from
// a failure — the reaction is the opposite.
func TestExitStopsFlow(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: guard
    exit: {reason: "not my case: {{kind}}"}
  - set: {var: unreachable, value: "no"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"kind": "small talk"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExit)

	var ee *ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, "not my case: small talk", ee.Reason, "the reason went through substitution")
	assert.Empty(t, vars["unreachable"], "steps after exit do not run")
}

func TestWhenBadConditionRejectedByValidate(t *testing.T) {
	_, _, err := ExecuteWith(context.Background(), parseFlow(t, `
steps:
  - name: x
    when: "garbage without an operator"
    set: {var: a, value: b}
`), Deps{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "when")
}

// on_server: the server is computed at execution time — 14 of 28 skills carry
// variants of one server, and without this each needs a switch of 2–5 branches.
func TestOnServerPicksServerAtRuntime(t *testing.T) {
	c := &recordingCaller{out: "logs"}
	f := parseFlow(t, `
tools: ["staging", "prod"]
steps:
  - name: logs
    on_server: "{{cluster}}"
    call:
      tool: get_logs
      args: {name: "pod-1"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"cluster": "prod"})
	require.NoError(t, err)
	assert.Equal(t, "prod", c.server, "the call went to the computed server")
	assert.Equal(t, "get_logs", c.tool)
}

// The computed name goes through THE SAME set check: substitution does not widen
// the access radius.
func TestOnServerCannotEscapeFlowTools(t *testing.T) {
	c := &recordingCaller{}
	f := parseFlow(t, `
tools: ["staging"]
steps:
  - name: logs
    on_server: "{{cluster}}"
    call:
      tool: get_logs
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"cluster": "prod-extra"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prod-extra")
	assert.Zero(t, c.calls)
}

// For a MODEL step on_server narrows the tool set down to a single server.
func TestOnServerNarrowsModelStepTools(t *testing.T) {
	r := &fakeRunner{}
	f := parseFlow(t, `
tools: ["staging", "prod", "sandbox"]
steps:
  - name: pick
    on_server: "{{cluster}}"
    instruction: "find the pod"
    save_as: pod
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, map[string]string{"cluster": "sandbox"})
	require.NoError(t, err)
	require.Len(t, r.seen, 1)
	assert.Equal(t, []string{"sandbox"}, r.seen[0].Tools,
		"the model sees the tools of ONE cluster, not of all three")
}

// The working-memory handle reaches the next step: without it a program can only
// work with what fitted into the preview, and large fetches (the full json of a
// cluster's pods) would have to be pushed through the model's context.
func TestCallStepExposesMemHandle(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call:
      tool: srv:get
      args: {}
    save_as: pods
  - name: crunch
    call:
      tool: srv:exec
      args: {stdin: {from: "{{pods.mem}}"}}
    save_as: out
`)
	c := &seqCaller{outs: []string{
		"preview…\n[mem:podsjson-a1b2 — this is a PREVIEW, 812kb in total]",
		"done",
	}}
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err)

	require.Len(t, c.seen, 2)
	stdin, ok := c.seen[1]["stdin"].(map[string]any)
	require.True(t, ok, "stdin is not an object: %#v", c.seen[1])
	assert.Equal(t, "podsjson-a1b2", stdin["from"], "the handle was not substituted")
}

// save_as next to call (rather than inside it) is how an author writes it: on a
// model step that is exactly where it lives. Losing it silently cost a judging
// search skill a verdict passed on an empty list, so the field is accepted at
// both levels.
func TestCallStepAcceptsStepLevelSaveAs(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call:
      tool: srv:get
      args: {}
    save_as: hits
`)
	require.NoError(t, f.Validate())
	c := &seqCaller{outs: []string{"result lines"}}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err)
	assert.Equal(t, "result lines", vars["hits"], "the call's result was not stored")
}

// Two records of the same field with different values are an error in the skill,
// not a reason to guess which of them is the real one.
func TestCallStepConflictingSaveAsIsError(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call:
      tool: srv:get
      args: {}
      save_as: inner
    save_as: outer
`)
	err := f.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "save_as given twice")
}

// builtin is the pseudo-server of built-in tools, not an MCP address: the flow's
// server set knows nothing about it, its radius is set by the skill's
// builtin_tools.
func TestCallStepAllowsBuiltinServer(t *testing.T) {
	f := parseFlow(t, `
tools: ["tracker"]
steps:
  - name: draw
    call:
      tool: builtin:run_script
      args: {name: chart_timeseries}
    save_as: chart
`)
	c := &seqCaller{outs: []string{"done"}}
	vars, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, nil)
	require.NoError(t, err, "builtin was rejected by the flow's radius")
	assert.Equal(t, "done", vars["chart"])
	require.Len(t, c.seen, 1)
}

// A radius refusal leaves a trace: under the continue policy the step otherwise
// vanished from the trace without a trace, and it looked like the step was
// absent from the skill.
func TestCallStepDeniedByScopeIsTraced(t *testing.T) {
	f := parseFlow(t, `
tools: ["tracker"]
steps:
  - name: sneak
    call:
      tool: vcs:get_file_contents
      args: {}
      on_error: continue
    save_as: out
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{Caller: &seqCaller{}}, nil)
	require.NoError(t, err)
	require.Len(t, outcome.Steps, 1, "the rejected step left no trace")
	assert.NotEqual(t, "ok", outcome.Steps[0].Outcome)
	assert.Contains(t, outcome.Steps[0].Reason, "vcs")
}
