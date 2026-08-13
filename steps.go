package skillengine

// Executing individual step kinds, and the failure policy.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
)

// stepName — the step's name for the trace and for progress. A name is
// optional, and an empty one is unreadable in a feed: for branches the role is
// visible from the kind.
func stepName(step Step) string {
	if step.Name != "" {
		return step.Name
	}
	return stepKind(step)
}

// stepKind — the step's kind, for the trace.
func stepKind(step Step) string {
	switch {
	case step.Call != nil:
		return "call"
	case step.Delegate != nil:
		return "delegate"
	case step.Parallel != nil:
		return "parallel"
	case step.ForEach != nil:
		return "for_each"
	case step.Exit != nil:
		return "exit"
	case step.Set != nil:
		return "set"
	case step.Switch != nil:
		return "switch"
	case step.If != nil:
		return "if"
	default:
		return "instruction"
	}
}

func (s *state) trace(step Step, outcome, reason string, calls int, started time.Time) {
	s.traceCalls(step, outcome, reason, calls, 0, started)
}

// traceCalls — a step trace splitting calls into all and failed.
func (s *state) traceCalls(step Step, outcome, reason string, calls, failed int, started time.Time) {
	kind := stepKind(step)
	name := stepName(step)
	tr := StepTrace{
		StartedAt: started,
		Name:      name, Kind: kind, Outcome: outcome,
		Reason: reason, Calls: calls, CallsFailed: failed, Duration: time.Since(started),
	}
	s.traces = append(s.traces, tr)
	if s.onStep != nil {
		s.onStep(tr)
	}
}

func (s *state) one(ctx context.Context, step Step) (bool, error) {
	started := time.Now()
	if s.onStepStart != nil {
		s.onStepStart(stepName(step), stepKind(step))
	}
	// The applicability condition is checked BEFORE the action: a step whose
	// precondition is false does not run at all — neither the model nor a tool
	// is called.
	if step.When != "" {
		ok, err := s.eval(step.When)
		if err != nil {
			return false, fmt.Errorf("step %q: when: %w", stepLabel(step), err)
		}
		if !ok {
			s.skipped = append(s.skipped, stepLabel(step))
			// A skip is not "nothing happened": it is the only trace of the
			// task having matched the skill only partially.
			s.trace(step, "skipped", "condition "+step.When+" is false", 0, started)
			return false, nil
		}
	}
	switch {
	case step.Set != nil:
		s.set(step.Set.Var, s.expand(step.Set.Value))
		s.trace(step, "ok", "", 0, started)
		return false, nil

	// A branch ABSORBS the skip signal: it means "skip the rest of the CURRENT
	// branch", not "abort the flow". Otherwise an optional step inside a branch
	// would take the whole remainder of the skill with it — for a full stop
	// there is abort.
	case step.Switch != nil:
		key := strings.TrimSpace(s.lookup(step.Switch.Var))
		branch, ok := step.Switch.Cases[key]
		chosen, outcome := key, "ok"
		if !ok {
			branch = step.Switch.Default
			// Falling through to default is a common cause of "the skill
			// answered the wrong thing": the value matched no branch. In the
			// trace that is visible immediately.
			chosen = "default (value " + key + ")"
			if len(branch) == 0 {
				// An empty default with non-empty cases is not "nothing to
				// do", it is a failed branch: the work the step existed for
				// was not done, while the flow carries on as if nothing
				// happened. A failure must be loud, otherwise it looks like
				// success (live case: verdict came out empty, no branch ran,
				// and the turn answered with an internal variable).
				outcome = "degraded"
				chosen = "no branch matched (value " + key + "), default is empty"
			}
		}
		s.trace(step, outcome, chosen, 0, started)
		_, err := s.run(ctx, branch)
		return false, err

	case step.If != nil:
		ok, err := s.eval(step.If.Cond)
		if err != nil {
			return false, err
		}
		if ok {
			s.trace(step, "ok", "then", 0, started)
			_, err = s.run(ctx, step.If.Then)
		} else {
			s.trace(step, "ok", "else", 0, started)
			_, err = s.run(ctx, step.If.Else)
		}
		return false, err

	case step.Delegate != nil:
		return s.delegateStep(ctx, step)

	case step.ForEach != nil:
		return s.forEachStep(ctx, step)

	case step.Parallel != nil:
		return s.parallelStep(ctx, step)

	case step.Exit != nil:
		reason := s.expand(step.Exit.Reason)
		s.trace(step, "exit", reason, 0, started)
		return false, &ExitError{Reason: reason}

	case step.Call != nil:
		return s.callStep(ctx, step)

	case step.Run != nil:
		return s.runStep(ctx, step)
	}
	return false, fmt.Errorf("skill-engine: step %q does nothing", step.Name)
}

func (s *state) runStep(ctx context.Context, step Step) (bool, error) {
	run := step.Run
	tools := s.toolsFor(run)
	if step.OnServer != "" {
		// The step's radius narrows to a single server: today a skill spanning
		// five clusters hands the model the tools of ALL five, and it can call
		// the wrong one. Narrowing makes the mistake impossible rather than
		// unlikely.
		only := s.expand(step.OnServer)
		if err := s.allowServer(only); err != nil {
			return s.onError(step, err)
		}
		tools = []string{only}
	}
	// A step that was handed no tools cannot follow a note telling it to fetch
	// the rest of a value, so it is given the whole thing instead (see
	// expandWhole). `on_server` above counts as having tools: it narrows the
	// radius to one server rather than removing it.
	instruction, unreachable := s.expand(run.Instruction), ""
	if len(tools) == 0 {
		instruction, unreachable = s.expandWhole(run.Instruction)
	}
	req := StepRequest{
		Name:           step.Name,
		Instruction:    instruction,
		Model:          run.Model,
		Sampling:       run.Sampling,
		ResponseSchema: run.ResponseSchema,
		OneOf:          run.OneOf,
		Tools:          tools,
		MaxCalls:       run.MaxCalls,
		MaxToolErrors:  run.MaxToolErrors,
	}
	started := time.Now()
	// Symmetric to the call and delegate paths, which have always said this: a
	// missing executor is the EMBEDDER's configuration error, and it has to
	// read like one. Without the check a model step dereferenced nil and took
	// the process down — a stack trace where "you did not pass Deps.Runner"
	// belonged, and no chance for the step's own on_error to have a say.
	if s.runner == nil {
		err := errors.New("instruction steps are unavailable: Deps.Runner is not set")
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	res, err := s.runner.Run(ctx, req)
	if err == nil {
		err = res.Err
	}
	if err != nil {
		s.traceCalls(step, outcomeFor(err), err.Error(), res.Calls, res.CallsFailed, started)
		return s.onError(step, err)
	}

	// The value, not the raw text: with `one_of` an ambiguous answer produces
	// text and stores nothing, and it is the stored value that flows on.
	value := normalizeOneOf(res.Text, run.OneOf, s.vocab.DecisionMarkers)
	policy, replacement := emptyPolicyOf(step)
	calls, failed := res.Calls, res.CallsFailed

	// on_empty: retry — run it again before deciding. The attempts are counted
	// into the trace: the step really did make those calls.
	if isBlankResult(value) && policy == EmptyRetry {
		for left := emptyRetriesOf(run); left > 0 && isBlankResult(value); left-- {
			again, aerr := s.runner.Run(ctx, req)
			calls, failed = calls+again.Calls, failed+again.CallsFailed
			if aerr == nil {
				aerr = again.Err
			}
			if aerr != nil {
				s.traceCalls(step, outcomeFor(aerr), aerr.Error(), calls, failed, started)
				return s.onError(step, aerr)
			}
			res.Note = again.Note
			value = normalizeOneOf(again.Text, run.OneOf, s.vocab.DecisionMarkers)
		}
	}

	// A `one_of` step that produced text and stored nothing, in an application
	// that declared no decision markers, is the one case where the missing
	// declaration COULD be the cause. Saying so is the difference between a
	// visible gap and a quiet default: without this the embedder sees a step
	// that "sometimes decides", and the reason is a field they never filled in.
	if isBlankResult(value) && len(run.OneOf) > 0 && strings.TrimSpace(res.Text) != "" &&
		len(s.vocab.DecisionMarkers) == 0 {
		res.Note = "the answer was prose and Vocabulary.DecisionMarkers is empty — " +
			"the engine has no words to find a decision by"
	}

	if isBlankResult(value) {
		switch policy {
		case EmptyUse:
			value = s.expand(replacement)
			s.traceCalls(step, "ok", "on_empty: used the declared value", calls, failed, started)
		// EmptyRetry lands here with its retries spent, and is treated as
		// EmptyFail: the author asked to retry because empty was not
		// acceptable, so carrying on now would be the very silence this
		// exists to break. `on_error: continue` next to it is how a skill
		// says "retry, then tolerate".
		case EmptyFail, EmptyRetry:
			s.traceCalls(step, "degraded", errEmptyResult.Error(), calls, failed, started)
			return s.onError(step, errEmptyResult)
		default:
			// EmptyContinue, and what the engine did before the field existed.
			s.traceEmptyContinue(step, res, calls, failed, started)
		}
	} else {
		// A step that finished without a single word is a failure, not a
		// success: the work it existed for was not done. Live class — a model
		// putting the answer into reasoning_content and leaving content empty;
		// the step was recorded as ok while the turn answered with an internal
		// variable. A failure must be loud.
		switch {
		// The executor's own reason is more precise than anything derived here
		// — it comes first.
		case res.Note != "":
			s.traceCalls(step, "degraded", res.Note, calls, failed, started)
		// A toolless step left with a fragment did its work on part of the data
		// and had no way to know it: there was no tool to fetch the rest and no
		// reader to resolve the handle. Nothing about the answer shows that, so
		// the trace has to — this is the quiet half of the very failure the
		// whole-value rule exists to remove.
		case unreachable != "":
			s.traceCalls(step, "degraded",
				fmt.Sprintf("%q was a preview and the whole value could not be read: "+
					"the step has no tools to fetch it and Deps.Memory did not resolve the handle", unreachable),
				calls, failed, started)
		// Every call failed — the step did NOT do its job, even if some text
		// came from the model. Otherwise a turn with seven rejected calls is
		// recorded as a success and the reason is hunted for in pod logs.
		case calls > 0 && failed == calls:
			s.traceCalls(step, "degraded",
				fmt.Sprintf("all tool calls failed (%d)", failed),
				calls, failed, started)
		default:
			s.traceCalls(step, "ok", "", calls, failed, started)
		}
	}

	// A step without save_as is the turn's final answer, not discarded work.
	// Its result used to be stored nowhere: the skill ran, there was nothing to
	// answer with, and the turn handed out the longest internal variable — that
	// is, the parse of the first step.
	target := run.SaveAs
	if target == "" {
		target = AnswerVar
	}
	if target != "" {
		s.set(target, value)
		s.noteAnswerWriter(target, stepKind(step))
	}
	return false, nil
}

// traceEmptyContinue records an empty result the skill tolerates, exactly as
// the engine did before on_empty existed: the executor's own reason wins,
// otherwise the generic one.
func (s *state) traceEmptyContinue(step Step, res Result, calls, failed int, started time.Time) {
	reason := res.Note
	if reason == "" {
		reason = "step produced no text"
	}
	s.traceCalls(step, "degraded", reason, calls, failed, started)
}

// callStep calls a tool directly, without the model.
func (s *state) callStep(ctx context.Context, step Step) (bool, error) {
	call := step.Call
	server, tool, _ := SplitToolRef(call.Tool)
	if step.OnServer != "" {
		// The server is named by the step — the tool name in call.tool may come
		// without a prefix. The computed name goes through the same set check.
		server = s.expand(step.OnServer)
		if _, bare, ok := SplitToolRef(call.Tool); ok {
			tool = bare
		} else {
			tool = call.Tool
		}
	}

	started := time.Now()
	// Failures BEFORE the call leave a trace too: without it a step rejected by
	// the radius vanished from the trace without a trace — under the continue
	// policy the flow moved on, and the events held neither the step nor a
	// reason: a live miss where two steps silently dropped out, and it looked
	// like they were absent from the skill.
	if err := s.allowServer(server); err != nil {
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	if s.caller == nil {
		err := errors.New("tool calls are unavailable")
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}

	out, err := s.caller.CallTool(ctx, server, tool, s.callArgs(call.Args))
	if err != nil {
		s.trace(step, outcomeFor(err), err.Error(), 1, started)
		return s.onError(step, err)
	}

	// A tool that returned nothing is the same class as a model that said
	// nothing — the emptiness travels on and the next step answers from it. The
	// mechanism therefore exists on both paths; `retry` is the one exception,
	// refused at validation because a call cannot be repeated.
	if policy, replacement := emptyPolicyOf(step); isBlankResult(out) {
		switch policy {
		case EmptyUse:
			out = s.expand(replacement)
			s.trace(step, "ok", "on_empty: used the declared value", 1, started)
		case EmptyFail:
			s.trace(step, "degraded", errEmptyResult.Error(), 1, started)
			return s.onError(step, errEmptyResult)
		default:
			// EmptyContinue: `ok`, as before this field existed. A tool
			// legitimately answers "nothing found", and the engine has no way
			// to tell that from a broken one.
			s.trace(step, "ok", "", 1, started)
		}
	} else {
		s.trace(step, "ok", "", 1, started)
	}
	if call.SaveAs != "" {
		s.set(call.SaveAs, out)
		s.noteAnswerWriter(call.SaveAs, stepKind(step))
		// A large result is put into working memory by the host, which returns
		// a preview with a handle. The handle itself is what the next step
		// needs to pass data BY REFERENCE: args: {stdin: {from: "{{name.mem}}"}}
		// — then a megabyte of json goes into the code, bypassing the model's
		// context. Without it a program can only work with what fits into a
		// preview.
		if id := memHandle(out); id != "" {
			s.set(call.SaveAs+MemSuffix, id)
		}
	}
	return false, nil
}

// delegateStep hands the work to another skill.
func (s *state) delegateStep(ctx context.Context, step Step) (bool, error) {
	d := step.Delegate
	started := time.Now()
	if s.delegate == nil {
		err := errors.New("delegation to skills is unavailable")
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	out, err := s.delegate.Delegate(ctx, d.Skill, s.expand(d.Task))
	if err != nil {
		s.trace(step, outcomeFor(err), err.Error(), 0, started)
		return s.onError(step, err)
	}
	// Delegation spawns a subagent — the most expensive operation of a turn.
	// Without a trace it is invisible in both events and the progress post: a
	// human stares at "thinking…" while another skill works for a minute.
	s.trace(step, "ok", "skill "+d.Skill, 1, started)
	if d.SaveAs != "" {
		s.set(d.SaveAs, out)
		s.noteAnswerWriter(d.SaveAs, stepKind(step))
	}
	return false, nil
}

// forEachStep repeats steps over a collection.
//
// The ceiling ALWAYS applies: a longer list is processed partially, and that is
// SAID OUT LOUD in the result. Silently processing half means handing out an
// answer that looks complete.
func (s *state) forEachStep(ctx context.Context, step Step) (bool, error) {
	fe := step.ForEach
	started := time.Now()
	// `in` resolves like ANY other reference — including a field
	// (`parts.stdout`). Direct variable access could not do that, and a loop
	// over an exec result walked the ENVELOPE {"exit_code":0,"stdout":"…"}
	// instead of the script's lines: one iteration instead of five. A review of
	// a 155-change MR came out empty, and it looked like "the model found
	// nothing".
	//
	// The collection is taken WHOLE: the variable holds a preview, and a
	// truncated list would give a partial walk that looks complete.
	items := splitCollection(s.fullValue(s.lookup(fe.In)))
	total := len(items)
	limit := fe.MaxIterations
	if limit <= 0 {
		limit = DefaultMaxIterations
	}
	if total > limit {
		items = items[:limit]
	}

	// The value of the variable named by collect is assembled here: a step
	// inside the loop writes into it via save_as, and the value is taken after
	// each iteration.
	var collected []string
	failed := 0
	for _, item := range items {
		s.set(fe.As, item)
		// Emptied BEFORE the body, so that what is collected is what THIS
		// iteration produced. The value used to be read after the body without
		// ever being cleared, and an iteration that wrote nothing — the branch
		// inside it did not fire — contributed whatever the previous iteration
		// had left there. A loop that picks three of five items out of a list
		// returned five, with two of them duplicates, and the duplicates look
		// exactly like honest results.
		//
		// It also fixes a stale read the other way round: a later step in the
		// same body reading the collect variable used to see the PREVIOUS
		// iteration's value where this one had written none.
		if fe.Collect != "" {
			s.set(fe.Collect, "")
		}
		if _, err := s.run(ctx, fe.Steps); err != nil {
			if errors.Is(err, ErrExit) {
				return false, err
			}
			failed++
			if skipped, oerr := s.onError(step, err); oerr != nil {
				s.trace(step, outcomeFor(oerr), oerr.Error(), len(items), started)
				return skipped, oerr
			}
			continue
		}
		if fe.Collect != "" {
			// Through the resolver, like every other consumer that is not the
			// model. Taken raw, an iteration whose last step was a `call` with a
			// large result would contribute a preview plus its "[mem:id]" note —
			// and once the iterations are joined those notes sit in the MIDDLE
			// of the string, where trimHostNote can no longer reach them (it
			// cuts the last line) and a single handle can no longer stand for N
			// results. Resolving before the join is the only moment this is
			// still fixable.
			collected = append(collected, s.payload(fe.Collect))
		}
	}
	if fe.Collect != "" {
		out := strings.Join(nonEmpty(collected), "\n\n")
		if total > limit {
			// Partial processing is SAID OUT LOUD: a silently processed half
			// gives an answer that looks complete.
			out += fmt.Sprintf("\n\n(processed %d of %d — hit the iteration ceiling)", limit, total)
		}
		s.set(fe.Collect, out)
	}
	// A loop is the most expensive step after delegation: N iterations, each
	// with its own calls. Without a trace neither their number nor how many
	// fell over is visible.
	outcome, reason := "ok", fmt.Sprintf("iterations: %d", len(items))
	if total > limit {
		outcome = "degraded"
		reason = fmt.Sprintf("iterations: %d of %d — ceiling", limit, total)
	}
	if failed > 0 {
		outcome = "degraded"
		reason += fmt.Sprintf(", failed: %d", failed)
	}
	s.trace(step, outcome, reason, len(items), started)
	return false, nil
}

// outcomeFor tells a permission refusal from any other failure: retrying the
// former is pointless, and in events they are different stories.
func outcomeFor(err error) string {
	if errors.Is(err, ErrDenied) {
		return "denied"
	}
	return "error"
}

// splitCollection parses a variable's value into a list of items: a JSON array
// or lines. The latter is the observed case: a list produced by a tool.
func splitCollection(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "[") {
		var arr []any
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, e := range arr {
				out = append(out, itemText(e))
			}
			return out
		}
	}
	return nonEmpty(strings.Split(v, "\n"))
}

// itemText — one element of a collection as the loop's body will see it.
//
// A string is itself; everything else goes back to JSON. Not fmt.Sprint: an
// array of OBJECTS — the natural output of a step with a response_schema — came
// out as Go's map formatting (`map[name:api restartCount:12]`), and the whole
// object went with it. Field access (`{{pod.name}}`, a condition on
// `pod.restartCount`) has nothing to parse and resolves to emptiness, while the
// model is shown a syntax belonging to the language the engine happens to be
// written in.
func itemText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parallelStep runs branches simultaneously.
//
// Each gets a COPY of the variables and its own state: branches do not see each
// other's work. Otherwise the outcome would depend on who finished first — and
// the format exists for predictability, so non-determinism here would be built
// into the construct.
//
// Only the variables produced by the branches make it back into the flow; a
// name conflict is resolved by declaration order (later wins), and that is a
// deliberate choice: the alternative is forbidding identical save_as, which
// gets in the way of symmetric branches like "search two sources".
func (s *state) parallelStep(ctx context.Context, step Step) (bool, error) {
	p := step.Parallel
	started := time.Now()
	type result struct {
		vars    map[string]string
		skipped []string
		err     error
	}
	results := make([]result, len(p.Branches))

	var wg sync.WaitGroup
	for i, branch := range p.Branches {
		wg.Add(1)
		go func(i int, branch []Step) {
			defer wg.Done()
			// FORKED from the flow's state, not assembled from a list of
			// fields. The list was the bug: six of them were missing — the
			// assets, their resolver, cache and context, working memory, and
			// the application's vocabulary — so an `{{asset:x}}` inside a
			// branch expanded to an empty string by contract, and the call
			// that needed it lost a required argument. Nothing failed; the
			// error pointed at the argument.
			//
			// A list has to be extended by whoever adds a field to `state`,
			// and the person adding a field is not thinking about `parallel`.
			// Forking inverts the default: everything reaches a branch unless
			// it is explicitly reset below, and what is reset is visible in
			// one place.
			forked := *s
			sub := &forked
			// What a branch must NOT inherit: the variables are its own copy
			// (the branches do not see each other's work — otherwise the
			// result would depend on who finished first), and the trace, the
			// skips and the answer belong to the branch alone until they are
			// merged back after the join.
			sub.vars = maps.Clone(s.vars)
			sub.seeded = maps.Clone(s.seeded)
			sub.skipped = nil
			sub.traces = nil
			sub.answeredBy = ""
			for k := range sub.vars {
				sub.seeded[k] = true // everything from before the fork is the branch's input
			}
			_, err := sub.run(ctx, branch)
			results[i] = result{vars: sub.produced(), skipped: sub.skipped, err: err}
		}(i, branch)
	}
	wg.Wait()

	var collected []string
	for _, r := range results {
		s.skipped = append(s.skipped, r.skipped...)
		if r.err != nil {
			// Exiting the skill is a decision for the whole turn, not for one
			// branch: it must not be swallowed by the continue policy.
			if errors.Is(r.err, ErrExit) {
				return false, r.err
			}
			if skipped, err := s.onError(step, r.err); err != nil {
				return skipped, err
			}
			continue
		}
		for k, v := range r.vars {
			s.set(k, v)
			if strings.TrimSpace(v) != "" {
				collected = append(collected, v)
			}
		}
	}
	if p.Collect != "" {
		s.set(p.Collect, strings.Join(collected, "\n\n"))
		// Skipped branches go into a separate variable `<collect>.skipped`, by
		// the same trick as `<save_as>.mem`. Without it the step that words the
		// answer sees only what was collected and cannot tell "the source
		// answered nothing" from "we never went to the source": live case — a
		// search skill wrote "the tracker has nothing on the topic" without
		// making a single query there.
		var skippedNames []string
		for _, r := range results {
			skippedNames = append(skippedNames, r.skipped...)
		}
		if len(skippedNames) > 0 {
			s.set(p.Collect+SkippedSuffix, strings.Join(skippedNames, ", "))
		}
	}
	// Branches trace themselves (a branch's state carries the same callback),
	// but the fork step itself does not, and the trace would lose the boundary:
	// how many branches went and how many of them failed. A failed branch under
	// the continue policy would otherwise be indistinguishable from one that
	// never started.
	failed, ran := 0, 0
	for _, r := range results {
		if r.err != nil {
			failed++
			continue
		}
		// A branch skipped by `when` did no work. Telling it apart from one
		// that ran matters because a fork where ALL branches were skipped
		// collected nothing — and the next step will word an answer anyway, and
		// it will look complete (live class: a search skill with not a single
		// probe selected answered just as confidently as with two).
		if len(r.skipped) == 0 || len(r.vars) > 0 {
			ran++
		}
	}
	outcome, reason := "ok", fmt.Sprintf("branches: %d", len(p.Branches))
	switch {
	case failed > 0:
		outcome = "degraded"
		reason = fmt.Sprintf("branches: %d, failed: %d", len(p.Branches), failed)
	case ran == 0 && len(p.Branches) > 0:
		outcome = "degraded"
		reason = fmt.Sprintf("branches: %d, none ran — nothing to collect", len(p.Branches))
	}
	s.trace(step, outcome, reason, len(p.Branches), started)
	return false, nil
}

// allowServer keeps a call inside the flow's set.
//
// Without this check a skill would go around its own restriction: the flow
// deliberately removed from the set a source that produced 53 attempts and not
// one successful read in a month — and a direct call would reach it anyway. A
// restriction that can be bypassed from inside restricts nothing.
func (s *state) allowServer(server string) error {
	// builtin is not an MCP server but the application's own built-in tools.
	// Their radius is set by the skill's builtin_tools field and checked before
	// execution (linter W7) plus by handing the registry to the executor: the
	// flow's set knows nothing about them and should not.
	if server == BuiltinServer {
		return nil
	}
	if len(s.tools) == 0 {
		// An empty flow set is NOT "everything is allowed". Symmetric to a
		// step, where an empty list means "hand out no tools at all"; the
		// opposite reading would make a skill without `servers` (an optional
		// field, both in the schema and in whatever writes skills) unrestricted —
		// including write tools.
		return fmt.Errorf("server %q: the flow declares no servers", server)
	}
	for _, t := range s.tools {
		if t == server {
			return nil
		}
	}
	return fmt.Errorf("server %q is outside the flow's set (%s)", server, strings.Join(s.tools, ", "))
}

// toolsFor computes a step's tool set: nil → the flow's set; a given one → the
// INTERSECTION with the flow's set (a step can only narrow).
//
// Widening is forbidden deliberately: otherwise a step could hand itself back a
// tool the flow removed on purpose, and the restriction would stop meaning
// anything.
func (s *state) toolsFor(run *Run) []string {
	if run.Tools == nil {
		return s.tools
	}
	want := *run.Tools
	if len(want) == 0 {
		return []string{} // the step gets no tools at all
	}
	if len(s.tools) == 0 {
		// An empty flow set hands out NOTHING, and a step cannot widen it back.
		// This used to return the step's own list, which put a server the flow
		// does not have in front of the model — the guard undone by the step it
		// was written against.
		//
		// The `call` path has always answered this the other way (see
		// allowServer, "an empty flow set is NOT everything is allowed"), and
		// two answers to one question is how a restriction stops meaning
		// anything. A flow that means to hand out tools says which.
		return []string{}
	}
	allowed := make(map[string]bool, len(s.tools))
	for _, t := range s.tools {
		allowed[t] = true
	}
	out := make([]string, 0, len(want))
	for _, t := range want {
		if allowed[t] {
			out = append(out, t)
		}
	}
	return out
}

// onError applies the step's failure policy.
func (s *state) onError(step Step, err error) (bool, error) {
	// A permission refusal is the most common class in live skills, and the
	// reaction is always the same: say so honestly and continue with what is
	// available. Workarounds and retries are pointless, they will not conjure
	// permissions.
	policy, saveAs := PolicyAbort, ""
	switch {
	case step.Run != nil:
		policy, saveAs = step.Run.OnError, step.Run.SaveAs
	case step.Call != nil:
		policy, saveAs = step.Call.OnError, step.Call.SaveAs
	case step.Delegate != nil:
		policy, saveAs = step.Delegate.OnError, step.Delegate.SaveAs
	case step.Parallel != nil:
		policy = step.Parallel.OnError
	case step.ForEach != nil:
		// Without this branch the field was parsed and silently ignored: the
		// loop died on the very first failure even though the skill asked to
		// mark the item and move on. Exactly the class no_retry was cut from
		// the format for.
		policy = step.ForEach.OnError
	}
	if policy == "" {
		policy = PolicyAbort
	}
	switch policy {
	case PolicyContinue:
		if saveAs != "" {
			s.set(saveAs, errText(err))
		}
		return false, nil
	case PolicySkip:
		if saveAs != "" {
			s.set(saveAs, errText(err))
		}
		return true, nil
	default:
		return false, fmt.Errorf("step %q: %w", stepLabel(step), err)
	}
}

func errText(err error) string {
	if errors.Is(err, ErrDenied) {
		return "DENIED: " + err.Error()
	}
	return "ERROR: " + err.Error()
}

func stepLabel(step Step) string {
	if step.Name != "" {
		return step.Name
	}
	return "unnamed"
}

// noteAnswerWriter records WHAT wrote the turn's answer: the model's text or a
// tool's output. There can be several writers to the answer variable (switch/if
// branches), so the last one is remembered — it is the one that stays.
func (s *state) noteAnswerWriter(target, kind string) {
	if target == AnswerVar {
		s.answeredBy = kind
	}
}
