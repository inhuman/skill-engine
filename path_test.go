package skillengine

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The answer of a real tool, in the shape a real tool returns it. The measured
// failure was a number three levels down in exactly this, and the author of the
// step wrote the path to it the way such paths are written everywhere.
const podDetails = `{
  "metadata": {"name": "api-7f9", "namespace": "prod"},
  "status": {
    "phase": "Running",
    "containerStatuses": [
      {"name": "api", "restartCount": 12, "ready": false},
      {"name": "sidecar", "restartCount": 0, "ready": true}
    ]
  }
}`

func TestSplitRef(t *testing.T) {
	for _, c := range []struct {
		ref   string
		base  string
		steps []pathStep
	}{
		{"pods", "pods", nil},
		{"pods.name", "pods", []pathStep{{field: "name"}}},
		{"a.b.c", "a", []pathStep{{field: "b"}, {field: "c"}}},
		{"pods[0]", "pods", []pathStep{{index: 0, byIdx: true}}},
		{"a.items[2].name", "a", []pathStep{{field: "items"}, {index: 2, byIdx: true}, {field: "name"}}},
	} {
		t.Run(c.ref, func(t *testing.T) {
			base, steps, ok := splitRef(c.ref)
			require.True(t, ok)
			assert.Equal(t, c.base, base)
			assert.Equal(t, c.steps, steps)
		})
	}

	for _, bad := range []string{"a.[0]", "a..b", "a.b.", "1a", "a[*]", "a[-1]", "a-b"} {
		_, _, ok := splitRef(bad)
		assert.Falsef(t, ok, "%q was accepted as a reference", bad)
	}
}

// The live case end to end: a number three levels inside a tool's answer,
// substituted into an instruction.
func TestDeepPathReachesTheInstruction(t *testing.T) {
	r := &fakeRunner{}
	f := parseFlow(t, `
steps:
  - name: report
    instruction: "{{pod.metadata.name}} restarted {{pod.status.containerStatuses[0].restartCount}} times"
    tools: []
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r},
		map[string]string{"pod": podDetails})
	require.NoError(t, err)
	assert.Equal(t, "api-7f9 restarted 12 times", r.seen[0].Instruction)
}

// The same path in a condition — the form five of six refusals took.
func TestDeepPathInACondition(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "pod.status.containerStatuses[0].restartCount > 0"
      then:
        - set: {var: verdict, value: "restarting"}
      else:
        - set: {var: verdict, value: "calm"}
  - name: second
    when: "pod.status.containerStatuses[1].ready == true"
    set: {var: sidecar, value: "ok"}
`)
	require.NoError(t, f.Validate())
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"pod": podDetails})
	require.NoError(t, err)
	assert.Equal(t, "restarting", vars["verdict"])
	assert.Equal(t, "ok", vars["sidecar"], "a boolean compares as the text it is written in")
}

// A value that is not a string comes back as the JSON it is: an object, a list,
// a number.
func TestPathYieldsJSONForStructures(t *testing.T) {
	s := &state{vars: map[string]string{"pod": podDetails}}
	assert.Equal(t, "prod", resolved(t, s, "pod.metadata.namespace"))
	assert.Equal(t, "12", resolved(t, s, "pod.status.containerStatuses[0].restartCount"))
	assert.Equal(t, `{"name":"api-7f9","namespace":"prod"}`, resolved(t, s, "pod.metadata"))
	assert.Contains(t, resolved(t, s, "pod.status.containerStatuses"), `"sidecar"`)
}

// A path that does not resolve is an ERROR, not an empty string. Silence here is
// worse than useless: `a.b.c` with `b` missing is indistinguishable from "the
// value is empty", and branches are taken on it.
func TestABrokenPathStopsTheTurn(t *testing.T) {
	f := parseFlow(t, `
steps:
  - name: report
    instruction: "{{pod.status.restarts}} times"
    tools: []
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: &fakeRunner{}},
		map[string]string{"pod": podDetails})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`pod.status` has no field `restarts`")
	assert.Contains(t, err.Error(), "containerStatuses", "the refusal must list what the object DOES have")
}

// Where exactly the walk broke, and why — the half of a refusal that turns it
// into a fix.
func TestTheRefusalSaysWhereTheWalkBroke(t *testing.T) {
	s := &state{vars: map[string]string{"pod": podDetails, "plain": "just text"}}
	for _, c := range []struct{ ref, want string }{
		{"pod.status.containerStatuses[9].name", "has 2 element(s), and there is no [9]"},
		{"pod.metadata[0].name", "`pod.metadata` is an object, not a list"},
		{"pod.metadata.name.x", "`pod.metadata.name` is a string, not an object"},
		{"pod.status.phase.deeper", "is a string, not an object"},
		{"plain.a.b", "`plain` is not JSON"},
		{"nowhere.a.b", "there is no variable `nowhere`"},
	} {
		t.Run(c.ref, func(t *testing.T) {
			_, err := s.resolve(c.ref)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			assert.Contains(t, err.Error(), c.ref, "the refusal must quote the reference as written")
		})
	}
}

// The silence one level deep is a PROMISE, not an oversight: skills written
// under it live in other people's storage and must not start failing on an
// engine upgrade. Anything deeper is new syntax and owes it nothing.
func TestOneLevelKeepsItsSilence(t *testing.T) {
	s := &state{vars: map[string]string{"pod": podDetails, "plain": "just text"}}
	for _, ref := range []string{"pod.nope", "plain.nope", "nowhere", "nowhere.nope"} {
		v, err := s.resolve(ref)
		require.NoErrorf(t, err, "%q became loud, and skills already written depend on it not being", ref)
		assert.Empty(t, v)
	}
}

// An exact name wins over a walk: the engine makes variables whose names contain
// a dot itself, and they are names, not paths.
func TestAnExactNameBeatsAPath(t *testing.T) {
	s := &state{vars: map[string]string{
		"pods":                  `{"mem": "not the handle"}`,
		"pods" + MemSuffix:      "res-7",
		"found":                 `{"skipped": "not this either"}`,
		"found" + SkippedSuffix: "in_docs",
	}}
	assert.Equal(t, "res-7", resolved(t, s, "pods.mem"))
	assert.Equal(t, "in_docs", resolved(t, s, "found.skipped"))
}

// A large result lives in a variable as a preview plus a handle, and the preview
// is truncated JSON that will never parse. A path reads the whole thing from
// working memory — the same fallback one-level access has always had.
func TestAPathReadsThroughWorkingMemory(t *testing.T) {
	s := &state{
		vars:   map[string]string{"pod": `{"status": {"contai` + "…\n[mem:res-9 — this is a PREVIEW, 42kb in total]"},
		memory: fakeMemory{"res-9": podDetails},
	}
	assert.Equal(t, "12", resolved(t, s, "pod.status.containerStatuses[0].restartCount"))
}

// Into ARGUMENTS a path goes the same way, and a broken one fails the step
// rather than handing a tool an empty argument.
func TestPathsInCallArguments(t *testing.T) {
	c := &recordingCaller{out: "done"}
	f := parseFlow(t, `
tools: ["k8s"]
steps:
  - call:
      tool: k8s:restart
      args: {name: "{{pod.metadata.name}}", ns: "{{pod.metadata.namespace}}"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Caller: c}, map[string]string{"pod": podDetails})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"name": "api-7f9", "ns": "prod"}, c.args)

	broken := parseFlow(t, `
tools: ["k8s"]
steps:
  - call:
      tool: k8s:restart
      args: {name: "{{pod.metadata.nome}}"}
      save_as: out
`)
	_, _, err = ExecuteWith(context.Background(), broken, Deps{Caller: c}, map[string]string{"pod": podDetails})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no field `nome`")
}

// A loop reads its collection through the same resolver, so the list may sit at
// the end of a path.
func TestForEachOverAPath(t *testing.T) {
	f := parseFlow(t, `
steps:
  - for_each:
      in: pod.status.containerStatuses
      as: c
      collect: names
      steps:
        - set: {var: names, value: "{{c.name}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"pod": podDetails})
	require.NoError(t, err)
	assert.Equal(t, "api\n\nsidecar", vars["names"])
}

// `[*]` was written in the same measurement as the paths. It is refused — a
// path resolves to ONE value — but the refusal names the loop that does what
// was asked, instead of listing the shapes that are allowed.
func TestASelectorIsRefusedWithAWayOut(t *testing.T) {
	_, _, _, err := parseCond("pod.status.containerStatuses[*].restartCount > 0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "for_each")
	assert.Contains(t, err.Error(), "list[0]")
}

// The form a model reaches for first: the path in braces, inside a condition.
// Braces are still refused, and the message still prints the condition without
// them — now for a path of any depth.
func TestBracedDeepPathNamesTheBraces(t *testing.T) {
	_, _, _, err := parseCond("{{pod.status.containerStatuses[0].restartCount}} > 0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`pod.status.containerStatuses[0].restartCount > 0`")
}

// A value large enough to be a whole tool result is clipped in the refusal: the
// sentence saying what is wrong has to survive it.
func TestTheNotJSONRefusalDoesNotPrintEverything(t *testing.T) {
	s := &state{vars: map[string]string{"log": strings.Repeat("line of a log ", 200)}}
	_, err := s.resolve("log.a.b")
	require.Error(t, err)
	assert.Less(t, len(err.Error()), 200)
}
