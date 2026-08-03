package skillengine

// Walking the flow: execution state and step order.
// Executing individual step kinds is in steps.go, conditions in cond.go,
// substitution in expand.go.

import (
	"context"
	"time"
)

// Deps — the executors the engine receives from outside. A struct rather than
// an argument list: there are several, and adding one more must not force an
// edit at every call site.
type Deps struct {
	Runner   Runner
	Caller   ToolCaller
	Delegate SkillDelegate
	// Assets resolves the content of declared payloads.
	Assets AssetResolver
	// Memory returns a tool's full result by its working-memory handle.
	//
	// Field substitution needs it: the host stores a PREVIEW in the variable
	// (truncated when large), while `{{var.field}}` parses the variable as
	// JSON. A truncated preview cannot be parsed at all, even though the whole
	// thing sits in memory under the same handle. Without this the field
	// silently goes empty — and the call leaves with an empty argument.
	//
	// Optional: nil = fields are only available on a result that was not
	// truncated.
	Memory MemoryReader
	// Vocabulary — the words of THIS application. The engine ships none of its
	// own: agents that share this format do not share a language, a domain or a
	// house style, and a word list baked in here would be one application's
	// habit imposed on everyone else's. See the Vocabulary type.
	Vocabulary Vocabulary
	// OnStepStart is called BEFORE a step. Needed by anyone showing work to a
	// human: a step that runs for 14 seconds emits no event until it
	// finishes, and there is nothing to show all that time.
	OnStepStart func(name, kind string)
	// OnStep is called RIGHT AFTER each step, not at the end of the flow.
	//
	// Otherwise the caller learns about all the steps at once, when the turn
	// is already over: the progress post shows a finished list instead of work
	// as it happens, and the user stares at "brewing…" for nine seconds.
	OnStep func(StepTrace)
}

// ExecuteWith walks the flow step by step and returns the variables PRODUCED
// by the steps. The input ones (`vars`) are not included: the caller has them
// already, whereas mistaking them for the result is expensive. Live class of
// failure: a flow that did not fill in the answer handed the caller its
// longest variable, which turned out to be the conversation history passed in
// — the user got a transcript of their own messages instead of an answer.
//
// An error is returned only when the flow was aborted (PolicyAbort or an
// executor failure). A step failing under another policy is not an error — it
// is recorded into the variables, deliberately: "no permission to read the
// attachment" should lead to an answer based on what is available, not to a
// failed turn.
func ExecuteWith(ctx context.Context, f *Flow, deps Deps, vars map[string]string) (map[string]string, Outcome, error) {
	st, err := newState(f, deps, vars)
	if err != nil {
		return nil, Outcome{}, err
	}
	st.assetCtx = ctx
	_, runErr := st.run(ctx, f.Steps)
	return st.produced(), Outcome{Skipped: st.skipped, Steps: st.traces, AnsweredBy: st.answeredBy}, runErr
}

// StepTrace — what happened to a single step.
//
// Collected by the engine and handed to the caller, who turns it into events.
// The engine itself does not emit telemetry — it is detachable and knows
// nothing about it.
type StepTrace struct {
	// StartedAt — when the step began. The caller needs it to place the
	// progress line at its chronological position: the event arrives on
	// COMPLETION but refers to the start of the work.
	StartedAt time.Time
	Name      string
	Kind      string // instruction | call | delegate | parallel | for_each | set | switch | if | exit
	Outcome   string // ok | skipped | denied | error | exit | truncated
	Reason    string
	Calls     int
	// CallsFailed — how many of the step's calls failed (see Result.CallsFailed).
	CallsFailed int
	Duration    time.Duration
}

// Outcome — what happened to the flow beyond the variables.
type Outcome struct {
	// Steps — the trace of every executed (and skipped) step.
	Steps []StepTrace
	// Skipped — steps not executed because of a false `when`. Empty for a flow
	// without conditions; non-empty means the task matched only PARTIALLY, and
	// this is the only way to notice that.
	Skipped []string
	// AnsweredBy — the kind of step that wrote the turn's ANSWER:
	// "instruction" (the model wrote the text) or "call" (a tool printed it).
	// The difference is not cosmetic: a model's answer is a draft and may
	// legitimately be rewritten "in voice"; the output of a deterministic
	// render is not a draft, and rewriting it means losing exactly the
	// guarantees it was made deterministic for.
	AnsweredBy string
}

func newState(f *Flow, deps Deps, vars map[string]string) (*state, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	st := &state{vars: map[string]string{}, tools: f.Tools,
		runner: deps.Runner, caller: deps.Caller, delegate: deps.Delegate,
		onStep: deps.OnStep, onStepStart: deps.OnStepStart,
		assets: f.Assets, assetsRes: deps.Assets, memory: deps.Memory,
		vocab:      deps.Vocabulary,
		assetCache: map[string]string{}, seeded: map[string]bool{}}
	for k, v := range f.Vars {
		st.vars[k] = v
		st.seeded[k] = true
	}
	for k, v := range vars {
		st.vars[k] = v
		st.seeded[k] = true
	}
	return st, nil
}

type state struct {
	vars     map[string]string
	tools    []string
	runner   Runner
	caller   ToolCaller
	delegate SkillDelegate
	// skipped — names of steps skipped by `when`. Returned to the caller:
	// without it a partial match of the task becomes invisible again, and that
	// is one of the two problems the format exists for.
	skipped []string
	// traces — the trace of every step, for the caller's events.
	traces []StepTrace
	// answeredBy — the kind of step that last wrote the turn's answer (see Outcome).
	answeredBy string
	// onStep / onStepStart — step notifications for the caller.
	onStep      func(StepTrace)
	onStepStart func(name, kind string)
	// assets — payload declarations and their resolver.
	assets    map[string]Asset
	assetsRes AssetResolver
	memory    MemoryReader
	// vocab — the embedding application's words (see Vocabulary).
	vocab Vocabulary
	// assetCache — content already fetched in THIS turn: one asset consumed by
	// three steps is fetched once.
	assetCache map[string]string
	// assetCtx — the turn's context for resolving payloads. expand() is called
	// from places with no ctx at hand, and threading it through every
	// signature for the sake of one branch costs more than storing it here:
	// state lives for exactly one turn.
	assetCtx context.Context
	// seeded — names of variables that came IN (the flow's vars and the
	// Execute argument). Steps read them, but they are not the flow's result.
	seeded map[string]bool
}

// produced returns the variables written by STEPS. A step that overwrote an
// input variable lands here — it produced it.
func (s *state) produced() map[string]string {
	out := make(map[string]string, len(s.vars))
	for k, v := range s.vars {
		if !s.seeded[k] {
			out[k] = v
		}
	}
	return out
}

// run executes a list of steps. Returns true if the branch was cut short by
// the skip policy (the caller decides whether to continue outside).
func (s *state) run(ctx context.Context, steps []Step) (bool, error) {
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		skipped, err := s.one(ctx, step)
		if err != nil {
			return false, err
		}
		if skipped {
			return true, nil
		}
	}
	return false, nil
}

// set writes a variable on behalf of a STEP: the value becomes part of the
// flow's result even if the name collides with an input one.
func (s *state) set(name, value string) {
	s.vars[name] = value
	delete(s.seeded, name)
}
