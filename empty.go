package skillengine

// What an empty step result means.
//
// Why. An empty result used to be indistinguishable from content downstream:
// the next step honestly ran `ok` on an empty input, and the user got an empty
// report wearing the look of a successful one. Live case at an embedder: a
// judging step returned nothing → the rendering step ran `ok` → the empty
// result reached the very end, and the failure surfaced at an external
// publication gate, a whole turn's work later.
//
// The step marks itself `degraded` even today, so the failure is not invisible
// — but a trace is read after the fact, while the flow needs to act on it
// before it wastes the rest of the turn.

import (
	"errors"
	"fmt"
	"strings"
)

// EmptyPolicy — what an empty result means for a step.
//
// A dictionary of outcomes rather than allow_empty: true/false, and
// deliberately the SAME SHAPE as ErrorPolicy: a boolean would have to grow the
// first time "empty is a valid answer" (there really were no findings) needs
// telling apart from "empty is a failure" — and it is already three outcomes,
// counting "empty, but use this instead". One vocabulary beats two.
type EmptyPolicy string

const (
	// EmptyContinue — an empty result is legal, the flow moves on. The default,
	// and exactly what the engine did before this field existed: an instruction
	// step is still traced `degraded`, a call step still `ok`. Declaring it
	// changes nothing; it is there so a skill can say the silence is intended.
	EmptyContinue EmptyPolicy = "continue"
	// EmptyFail — the step counts as failed, and OnError decides from there.
	EmptyFail EmptyPolicy = "fail"
	// EmptyRetry — run the step again, up to OnEmptyRetries times. Instruction
	// steps only: repeating a `call` would break the format's promise that a
	// call step cannot be repeated, and a retried call with a side effect is a
	// second ticket, a second merge request, a second e-mail.
	//
	// Still empty once the retries are spent → treated as EmptyFail: the author
	// asked to retry because empty was not acceptable, and continuing then is
	// the very silence this exists to break. Pair it with `on_error: continue`
	// to retry and then tolerate.
	EmptyRetry EmptyPolicy = "retry"
	// EmptyUse — store OnEmptyValue instead and carry on. Supports {{var}}.
	EmptyUse EmptyPolicy = "use"
)

// DefaultEmptyRetries — how many times EmptyRetry runs the step again when the
// skill does not say. One, because "retry" means once unless stated otherwise.
const DefaultEmptyRetries = 1

// MaxEmptyRetries — the ceiling on that count. The format has no unlimited
// anything on purpose: a step retried without a bound brings back exactly the
// runaway the format exists to prevent. Five matches the ceiling already
// chosen for fetching an external asset.
const MaxEmptyRetries = 5

// errEmptyResult — the failure EmptyFail hands to the step's OnError, so an
// empty result travels the same road as any other failed step (marked ERROR:,
// counted, subject to abort/continue/skip) instead of inventing a second one.
var errEmptyResult = errors.New("step produced an empty result")

// isBlankResult — the emptiness predicate: an empty string after TrimSpace.
//
// Applied to the value the step WOULD STORE, not to the model's raw text. The
// difference is not academic: a classifier with `one_of` whose answer was
// ambiguous produces text and stores nothing — normalizeOneOf refuses to guess
// — and that empty value is precisely what flows downstream and sends the
// switch to its default.
//
// Nothing deeper is recognised. An empty JSON object or an empty array in one
// field would mean the engine started interpreting content, which it does
// nowhere else; that judgement belongs to whoever consumes the result.
func isBlankResult(v string) bool { return strings.TrimSpace(v) == "" }

// emptyPolicyOf returns the step's policy and the value for EmptyUse. Written
// on the step for an instruction, inside `call:` for a call — the same place
// as on_error, so there is one habit to learn rather than two.
func emptyPolicyOf(step Step) (EmptyPolicy, string) {
	switch {
	case step.Call != nil:
		return orContinue(step.Call.OnEmpty), step.Call.OnEmptyValue
	case step.Run != nil:
		return orContinue(step.Run.OnEmpty), step.Run.OnEmptyValue
	}
	return EmptyContinue, ""
}

func orContinue(p EmptyPolicy) EmptyPolicy {
	if p == "" {
		return EmptyContinue
	}
	return p
}

func emptyRetriesOf(run *Run) int {
	if run == nil || run.OnEmptyRetries <= 0 {
		return DefaultEmptyRetries
	}
	return run.OnEmptyRetries
}

// validateEmptyPolicy checks the declaration before execution: a policy that
// needs a value must have one, and a value or a count that no policy reads is a
// field the author believes in and the engine ignores.
func validateEmptyPolicy(p EmptyPolicy, value string, retries int, isCall bool, at string) error {
	switch p {
	case "", EmptyContinue, EmptyFail, EmptyUse:
	case EmptyRetry:
		if isCall {
			// Not a matter of taste: "a call step cannot be repeated" is a
			// promise the format makes in as many words, and skills are written
			// against it.
			return fmt.Errorf("%s: on_empty: retry on a call step — a call cannot be repeated; use fail, continue or use", at)
		}
	default:
		return fmt.Errorf("%s: unknown on_empty %q", at, p)
	}

	if p == EmptyUse && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: on_empty: use without on_empty_value — there is nothing to substitute", at)
	}
	if p != EmptyUse && value != "" {
		return fmt.Errorf("%s: on_empty_value is set but on_empty is not `use` — the value would never be used", at)
	}
	if retries != 0 {
		if p != EmptyRetry {
			return fmt.Errorf("%s: on_empty_retries is set but on_empty is not `retry` — the count would never be read", at)
		}
		if retries < 0 || retries > MaxEmptyRetries {
			return fmt.Errorf("%s: on_empty_retries %d outside 1..%d", at, retries, MaxEmptyRetries)
		}
	}
	return nil
}
