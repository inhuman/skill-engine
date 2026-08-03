package skillengine

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shape a host hands back for a large result: a fragment, then a note
// saying where the rest is and how to fetch it. The wording is the host's —
// declared through Vocabulary — and so is the tool it names.
const (
	bigPreview = "первый комментарий…\n[mem:abc — this is a PREVIEW, 18kb in total. " +
		"Whole: memory(op=peek, id=\"abc\").]"
	bigWhole = "первый комментарий\nвторой комментарий\n…восемнадцать килобайт…"
)

func previewFlow(t *testing.T) *Flow {
	t.Helper()
	return parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:get", save_as: ticket}
  - name: report
    instruction: "Ticket: {{ticket}}"
    tools: []
`)
}

func previewDeps(r *fakeRunner) Deps {
	return Deps{
		Runner: r,
		Caller: ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
			return bigPreview, nil
		}),
		Memory:     fakeMemory{"abc": bigWhole},
		Vocabulary: Vocabulary{TruncationNotes: []string{"truncated:"}},
	}
}

// A step declared WITHOUT tools cannot follow the note: there is no tool to
// call and never will be. Handed one anyway, a model does the only thing left —
// it writes the call out as its answer, and the turn ends with a fragment of
// JSON where a report was meant. The step reports `ok`, the counters are clean,
// and the user is the first to see it.
//
// Live failure: a ticket-reporting step answered with
// {"id":"…","op":"grep","pattern":"…"} — the arguments of a memory call,
// printed as prose.
func TestStepWithoutToolsGetsTheWholeValue(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	_, _, err := ExecuteWith(context.Background(), previewFlow(t), previewDeps(r), nil)
	require.NoError(t, err)
	require.Len(t, r.seen, 1)

	got := r.seen[0].Instruction
	assert.Contains(t, got, "восемнадцать килобайт", "the step cannot fetch the rest, so it must be given it")
	assert.NotContains(t, got, "memory(op=peek",
		"a step with no tools was told to make a call it cannot make")
	assert.NotContains(t, got, "[mem:abc", "the handle is useless to a step that cannot resolve it")
}

// A step WITH tools keeps the preview and the note: that is the case the note
// was written for, and the model completes it by calling the tool.
func TestStepWithToolsKeepsThePreview(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:get", save_as: ticket}
  - name: report
    instruction: "Ticket: {{ticket}}"
    tools: ["srv"]
`)
	_, _, err := ExecuteWith(context.Background(), f, previewDeps(r), nil)
	require.NoError(t, err)

	got := r.seen[0].Instruction
	assert.Contains(t, got, "memory(op=peek", "the step can follow the note — it must still get it")
	assert.NotContains(t, got, "восемнадцать килобайт")
}

// A step that names no tools of its own inherits the FLOW's set, and a flow
// without tools leaves it with none — the same case, reached differently.
func TestStepInheritsAnEmptyFlowSet(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	f := parseFlow(t, `
steps:
  - name: seed
    set: {var: ticket, value: "x"}
  - name: report
    instruction: "Ticket: {{ticket}}"
`)
	s, err := newState(f, previewDeps(r), map[string]string{})
	require.NoError(t, err)
	s.vars["ticket"] = bigPreview

	_, err = s.runStep(context.Background(), f.Steps[1])
	require.NoError(t, err)
	assert.Contains(t, r.seen[0].Instruction, "восемнадцать килобайт")
}

// `on_server` gives a step the tools of one server, so it is a step WITH tools
// however its own list looks.
func TestOnServerCountsAsHavingTools(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:get", save_as: ticket}
  - name: report
    instruction: "Ticket: {{ticket}}"
    on_server: srv
    tools: []
`)
	_, _, err := ExecuteWith(context.Background(), f, previewDeps(r), nil)
	require.NoError(t, err)
	assert.Contains(t, r.seen[0].Instruction, "memory(op=peek")
}

// An asset substituted into a toolless step is unaffected: assets are resolved
// whole to begin with, and the rule is about a variable holding a preview.
func TestWholeValueRuleLeavesShortValuesAlone(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	f := parseFlow(t, `
steps:
  - name: seed
    set: {var: ticket, value: "short and whole"}
  - name: report
    instruction: "Ticket: {{ticket}}"
    tools: []
`)
	_, _, err := ExecuteWith(context.Background(), f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Ticket: short and whole", strings.TrimSpace(r.seen[0].Instruction))
}

// The whole value may be unreachable: a fragment carrying a handle, and no
// reader to resolve it. The step still runs — the fragment is better than an
// instruction it cannot carry out — but it worked on part of the data without
// any way to know, and that is exactly the quiet half of the failure this rule
// exists to remove. So the trace says it.
func TestUnreachableWholeValueIsReported(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	deps := previewDeps(r)
	deps.Memory = nil // the host gave no reader

	_, outcome, err := ExecuteWith(context.Background(), previewFlow(t), deps, nil)
	require.NoError(t, err)

	var report *StepTrace
	for i := range outcome.Steps {
		if outcome.Steps[i].Name == "report" {
			report = &outcome.Steps[i]
		}
	}
	require.NotNil(t, report)
	assert.Equal(t, "degraded", report.Outcome, "a step that saw part of the data was recorded as fine")
	assert.Contains(t, report.Reason, "ticket")
	assert.Contains(t, report.Reason, "Deps.Memory")

	// Even then the impossible instruction does not go out: a note telling the
	// step to call something is worse than a fragment.
	assert.NotContains(t, r.seen[0].Instruction, "memory(op=peek")
}

// A reader that no longer has the handle is the same case as no reader at all.
func TestForgottenHandleIsReported(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	deps := previewDeps(r)
	deps.Memory = fakeMemory{"other": "something else"}

	_, outcome, err := ExecuteWith(context.Background(), previewFlow(t), deps, nil)
	require.NoError(t, err)
	assert.Equal(t, "degraded", outcome.Steps[1].Outcome)
}

// A value that never was a preview resolves cleanly, and the step stays ok:
// the report must not fire on every toolless step.
func TestWholeValueWithoutAHandleIsNotReported(t *testing.T) {
	r := &fakeRunner{answer: map[string]string{"report": "the report"}}
	f := parseFlow(t, `
tools: ["srv"]
steps:
  - name: fetch
    call: {tool: "srv:get", save_as: ticket}
  - name: report
    instruction: "Ticket: {{ticket}}"
    tools: []
`)
	deps := previewDeps(r)
	deps.Caller = ToolCallerFunc(func(context.Context, string, string, map[string]any) (string, error) {
		return "the whole small answer", nil
	})

	_, outcome, err := ExecuteWith(context.Background(), f, deps, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", outcome.Steps[1].Outcome)
	assert.Empty(t, outcome.Steps[1].Reason)
}
