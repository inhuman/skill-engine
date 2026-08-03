package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A value that QUOTES arbitrary text — a diff, a log, a user's message — may
// contain anything, including the engine's own markers. This chunk is ordinary
// code: it is the code that appends a working-memory handle, so it names one.
const diffChunk = "@@ -160,7 +160,7 @@ func note(text, id string) string {\n" +
	"-\treturn text + fmt.Sprintf(\"\\n[mem:%s]\", id)\n" +
	"+\treturn text + fmt.Sprintf(\"\\n[mem:kget-a3f7]\", id)\n" +
	" }"

// The handle is written by the host on the LAST line. Looked for anywhere in
// the text, a quotation becomes a control marker — and the failure lands
// exactly on the repositories where this mechanism is implemented, that is, on
// any embedder reviewing its own code.
func TestHandleIsReadWhereTheHostWritesIt(t *testing.T) {
	assert.Empty(t, memHandle(diffChunk),
		"a handle quoted INSIDE the data was taken for the host's note")

	assert.Equal(t, "abc", memHandle("data\n[mem:abc]"), "the note is on the last line")
	assert.Equal(t, "abc", memHandle("data\n[mem:abc — this is a PREVIEW, 42kb in total]"))
	assert.Equal(t, "abc", memHandle("data\n[mem:abc]\n"), "a trailing newline is not the last line")
	assert.Equal(t, "abc", memHandle("[mem:abc]"), "an empty result is nothing but its handle")
	assert.Empty(t, memHandle("no handle at all"))
}

// The step's own result is quoted content too. Reading a handle out of its body
// puts a FOREIGN id into `<var>.mem`, and `{from: "{{var.mem}}"}` then hands a
// tool somebody else's data — silently, because a handle looks like a handle.
//
// This one is older than the rule about toolless steps: it is on the `call`
// path, where the handle is stored.
func TestQuotedHandleDoesNotBecomeTheVariablesHandle(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:diff", save_as: chunk}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return diffChunk, nil
		}),
	}, nil)
	require.NoError(t, err)

	assert.NotContains(t, vars, "chunk"+MemSuffix,
		"an id quoted in the data became the variable's handle")
}

// A real note still reaches `<var>.mem` — that is what it is appended for.
func TestRealHandleStillReachesTheVariable(t *testing.T) {
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:diff", save_as: chunk}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return diffChunk + "\n[mem:kget-real]", nil
		}),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "kget-real", vars["chunk"+MemSuffix])
}

// The whole value was there all along, so a step without tools must run on it
// and stay `ok`. Reported as degraded, a review falls apart on exactly the
// chunks that quote this mechanism — and it looks like a flake, because it
// depends on which chunk got which lines.
func TestQuotedHandleDoesNotDegradeAToollessStep(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"judge": "the review"}}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:diff", save_as: chunk}
  - name: judge
    instruction: "Review: {{chunk}}"
    tools: []
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{
		Runner: r,
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return diffChunk, nil
		}),
		Memory: fakeMemory{"a-real-one": "data"},
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, "ok", outcome.Steps[1].Outcome, "a whole value was reported as an unreadable preview")
	assert.Empty(t, outcome.Steps[1].Reason)
	assert.Contains(t, r.seen[0].Instruction, "kget-a3f7", "the quoted code must reach the reviewer intact")
}

// A genuinely stale note — the host promised data and working memory no longer
// has it — is still reported: that is a real loss, not a quotation.
func TestStaleHandleIsStillReported(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"judge": "the review"}}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:diff", save_as: chunk}
  - name: judge
    instruction: "Review: {{chunk}}"
    tools: []
`)
	_, outcome, err := ExecuteWith(context.Background(), f, Deps{
		Runner: r,
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return "fragment…\n[mem:gone]", nil
		}),
		Memory: fakeMemory{"other": "data"},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "degraded", outcome.Steps[1].Outcome)
	assert.Contains(t, outcome.Steps[1].Reason, "chunk")
}

// Passing data by reference must follow the same rule: the argument gets the
// whole value, and a quoted marker inside it is data, not an instruction.
func TestQuotedHandleDoesNotDivertACallArgument(t *testing.T) {
	var got map[string]any
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:diff", save_as: chunk}
  - name: render
    call:
      tool: "srv:render"
      args: {stdin: "{{chunk}}"}
      save_as: out
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{
		Caller: ToolCallerFunc(func(_ context.Context, _, tool string, args map[string]any) (string, error) {
			if tool == "render" {
				got = args
				return "done", nil
			}
			return diffChunk, nil
		}),
		Memory: fakeMemory{"kget-a3f7": "SOMEBODY ELSE'S DATA"},
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, got["stdin"], "kget-a3f7")
	assert.NotContains(t, got["stdin"], "SOMEBODY ELSE'S DATA",
		"a marker quoted in the data fetched a foreign value from working memory")
}
