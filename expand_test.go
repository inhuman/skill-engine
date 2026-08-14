package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Accessing a field of a structured result: a step extracts several values with
// ONE call instead of three — otherwise the point of a structured answer is
// lost.
func TestExpandStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - set: {var: msg, value: "cluster {{target.cluster}}, ns {{target.namespace}}"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"target": `{"cluster":"staging","namespace":"backend","replicas":3}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "cluster staging, ns backend", vars["msg"])
}

// A non-string field is substituted as JSON: a number still looks like a number.
func TestExpandStructuredNonStringField(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "replicas: {{t.replicas}}"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"t": `{"replicas":3}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "replicas: 3", vars["out"])
}

// A missing field yields an empty string, same as a missing variable: a marker
// that reached the model would read as part of the instruction.
func TestExpandMissingFieldIsEmpty(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "[{{t.nope}}][{{missing.field}}]"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"t": `{"a":1}`})
	require.NoError(t, err)
	assert.Equal(t, "[][]", vars["out"])
}

// A variable with a dot in its NAME beats parsing "object.field": an explicit
// name outranks a guess.
func TestExpandPrefersLiteralName(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "{{a.b}}"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{
		"a.b": "literal",
		"a":   `{"b":"from the object"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "literal", vars["out"])
}

// Non-JSON in a variable does not break substitution.
func TestExpandFieldOfNonJSON(t *testing.T) {
	f := parseFlow(t, `steps: [{set: {var: out, value: "[{{t.field}}]"}}]`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{}, map[string]string{"t": "just text"})
	require.NoError(t, err)
	assert.Equal(t, "[]", vars["out"])
}

// A condition on a FIELD of a structured result.
//
// Live case: switch was taught to read a field, and the condition was forgotten.
// The measurement showed TWO calls instead of three and zero trips to the
// repository — that is, "it got cheaper" instead of "it broke": the "resource
// not named" branch was always chosen, because `req.resource is not empty` was
// silently false.
func TestConditionOnStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "req.resource is not empty"
      then:
        - set: {var: out, value: "have {{req.resource}}"}
      else:
        - set: {var: out, value: "none"}
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"resource":"t1_kaas","owner":"t1"}`})
	require.NoError(t, err)
	assert.Equal(t, "have t1_kaas", vars["out"])

	vars, _, err = ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"owner":"t1"}`})
	require.NoError(t, err)
	assert.Equal(t, "none", vars["out"], "no field — the condition is false")
}

func TestSwitchOnStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - switch:
      var: req.owner
      cases:
        t1: [{set: {var: out, value: "ours"}}]
        foreign: [{set: {var: out, value: "theirs"}}]
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"owner":"foreign"}`})
	require.NoError(t, err)
	assert.Equal(t, "theirs", vars["out"])
}

// Comparing by field takes the same road.
func TestEqualityOnStructuredField(t *testing.T) {
	f := parseFlow(t, `
steps:
  - if:
      cond: "req.genre == write"
      then: [{set: {var: out, value: "writing config"}}]
      else: [{set: {var: out, value: "analysing"}}]
`)
	vars, _, err := ExecuteWith(context.Background(), f, Deps{},
		map[string]string{"req": `{"genre":"write"}`})
	require.NoError(t, err)
	assert.Equal(t, "writing config", vars["out"])
}

// fakeMemory — the turn's working memory in tests.
type fakeMemory map[string]string

func (m fakeMemory) Get(id string) (string, bool) { v, ok := m[id]; return v, ok }

// The host appends a handle to ANY tool result, and the variable stops being
// valid JSON. Without stripping the note, `{{var.field}}` did not work ONCE on
// any `call:` result — the field silently went empty.
func TestFieldLookupIgnoresHostMemNote(t *testing.T) {
	s := &state{vars: map[string]string{
		"ctx": `{"head_sha":"abc123","delta_scope":"go"}` + "\n[mem:res-1]",
	}}
	assert.Equal(t, "abc123", resolved(t, s, "ctx.head_sha"))
	assert.Equal(t, "go", resolved(t, s, "ctx.delta_scope"))
}

// In a truncated preview the JSON is incomplete and will never parse. The whole
// of it sits in working memory under the same handle — that is where it comes
// from.
//
// Live case: a context prefetch came out larger than the preview threshold, and
// the steps below got empty arguments instead of fields — the transport rejected
// the calls, and the step that needed their output was left with nothing.
func TestFieldLookupFallsBackToWorkingMemory(t *testing.T) {
	full := `{"head_sha":"deadbeef","delta_regex":"^(a|b)$","delta_scope":"go"}`
	s := &state{
		vars:   map[string]string{"ctx": `{"head_sha":"dead` + "…\n[mem:res-9 — this is a PREVIEW, 42kb in total]"},
		memory: fakeMemory{"res-9": full},
	}
	assert.Equal(t, "deadbeef", resolved(t, s, "ctx.head_sha"))
	assert.Equal(t, "^(a|b)$", resolved(t, s, "ctx.delta_regex"))
}

// No memory — the field is empty, but that does not bring the turn down.
func TestFieldLookupWithoutMemoryStaysEmpty(t *testing.T) {
	s := &state{vars: map[string]string{"ctx": `{"head_sha":"dead` + "…\n[mem:res-9]"}}
	assert.Equal(t, "", resolved(t, s, "ctx.head_sha"),
		"one level deep keeps the silence it was promised")
}

// Into ARGUMENTS a value goes whole and without the host's note: there it is
// read by a script, not by the model. The "[mem:id]" handle helps the model and
// breaks parsing on the other end.
//
// Live case: render received findings with the tail and answered "RENDER_ERROR:
// stdin does not parse as a JSON stream".
func TestArgsGetCleanFullValue(t *testing.T) {
	s := &state{
		vars:   map[string]string{"findings": `{"a":1` + "…\n[mem:res-3 — this is a PREVIEW, 42kb in total]"},
		memory: fakeMemory{"res-3": `{"a":1,"b":2}`},
	}
	args := expandedArgs(t, s, map[string]any{"stdin": "{{findings}}"})
	assert.Equal(t, `{"a":1,"b":2}`, args["stdin"], "whole, from memory, without the note")
}

// A note the host DECLARED is stripped, whatever it says and whatever script it
// is written in: the engine keeps no wording of its own, so this is the only way
// it can tell a remark about the data from the data.
func TestArgsTrimDeclaredHostNote(t *testing.T) {
	for _, note := range []string{"[обрезано: 42kb]", "[gekürzt: 42kb]", "[shortened: 42kb]"} {
		s := &state{
			vars:  map[string]string{"findings": `{"a":1}` + "\n" + note},
			vocab: Vocabulary{TruncationNotes: []string{"обрезано:", "gekürzt:", "shortened:"}},
		}
		args := expandedArgs(t, s, map[string]any{"stdin": "{{findings}}"})
		assert.Equal(t, `{"a":1}`, args["stdin"], note)
	}
}

// An UNDECLARED note travels on into the argument — and that is the honest
// outcome rather than a bug: the engine cannot tell somebody's remark from
// somebody else's payload, and guessing at it would corrupt the data of every
// host whose results legitimately end in a bracketed line.
func TestArgsKeepAnUndeclaredNote(t *testing.T) {
	s := &state{vars: map[string]string{"findings": `{"a":1}` + "\n[gekürzt: 42kb]"}}
	args := expandedArgs(t, s, map[string]any{"stdin": "{{findings}}"})
	assert.Equal(t, `{"a":1}`+"\n[gekürzt: 42kb]", args["stdin"])
}

// The handle the FORMAT defines needs no declaration: the engine writes it and
// resolves it, so it can recognise it.
func TestArgsTrimTheFormatsOwnHandle(t *testing.T) {
	s := &state{vars: map[string]string{"findings": `{"a":1}` + "\n[mem:res-3]"}}
	args := expandedArgs(t, s, map[string]any{"stdin": "{{findings}}"})
	assert.Equal(t, `{"a":1}`, args["stdin"])
}

// In an INSTRUCTION the note stays: it is how the model learns the data is
// truncated and how to read the rest.
func TestInstructionKeepsMemHandle(t *testing.T) {
	s := &state{vars: map[string]string{"ctx": "data\n[mem:res-9]"}}
	out, err := s.expand("here is {{ctx}}")
	require.NoError(t, err)
	assert.Contains(t, out, "[mem:res-9]")
}

// resolved — a reference the test expects to resolve. A path that broke is an
// error, and a test that swallowed it would be asserting about the empty string
// the failure left behind.
func resolved(t *testing.T, s *state, ref string) string {
	t.Helper()
	v, err := s.resolve(ref)
	require.NoError(t, err)
	return v
}

func expandedArgs(t *testing.T, s *state, args map[string]any) map[string]any {
	t.Helper()
	out, err := s.expandArgs(args)
	require.NoError(t, err)
	return out
}
