# Changes to the skill format

The format version is `EngineVersion` in `version.go`; a skill declares the
minimum it requires in `skill_engine_version`. The engine refuses a foreign
major in both directions: a description of a previous major would parse without
a single complaint, silently losing fields the structs no longer have.

Because skills live in a user's storage and are not updated with a deploy, a
major that needs edits ships with the code that makes them: `Migrate(raw)`
rewrites a skill file into the format this engine speaks and reports whether
anything changed. It edits the text — comments, key order and block scalars
survive — and it does not validate the result, so parse and validate as usual
afterwards.

```go
out, changed, err := skillengine.Migrate(raw)
```

The input is a skill file as the format defines it: one YAML document. If you
wrap skills in something of your own — front matter, a markdown body, several
documents in one file — unwrap before calling and wrap the result back;
anything else is refused rather than guessed at.

## 2.3.0

Four new condition forms. A skill that does not use them behaves exactly as it
did, so nothing needs migrating; a skill that uses one must declare
`skill_engine_version: 2.3.0`, which is what stops an older engine from reading
it and refusing a condition it cannot parse.

- **Added**: `var > 5`, `var >= other.var`, `var < 0.5`, `var <= days` alongside
  `==`, `!=`, `is [not] empty` and `contains`. The right side may be a number or
  the NAME of a variable holding one — a threshold is rarely a constant, it
  arrives from the step that parsed the request.

  Why. Numbers were in the flow all along — counters, ids, thresholds — and the
  only thing expressible about one was equality with a literal, which is why a
  live catalogue spells "no pipeline found" as `ci.id == 0`. Everything else
  went one of two ways: into the TEXT of a step, where a deterministic rule ends
  up applied by a model («порог простоя больше нуля — оставь только те, что не
  двигались дольше него»), or into an asset — a network call to compare two
  numbers.

  The refusal was not hypothetical. On the first day skills were being written
  by a model rather than by hand, it wrote `{{pod.restartCount}} > 5`, got a
  parse error, and wrote the same form again in another step of the same
  session. Both attempts were a condition in the body of a `for_each`: "keep the
  ones over the threshold" is written as a loop with a branch inside, which is
  why this closes the case and no collection filter is added.

  Both ways of not being a number are LOUD — an error that stops the turn, not a
  `false` that quietly takes the other branch:

  - a value that does not parse as a number names itself in the error;
  - an empty or failed variable is **not zero**. Reading it as 0 makes "the step
    returned nothing" indistinguishable from "the number is small" — the same
    failure `is empty` exists to prevent. Where emptiness is legal, the author
    writes `var is not empty` in front.

  A condition is the one place where a wrong answer leaves no trace:
  `restarts > 5` looks right whatever it returns, which is the class the
  linter's W14 was written for.

  Two integers are compared as integers, so a nineteen-digit id does not lose
  its last digits to float64. `NaN`, `Inf` and hex floats are not numbers here:
  a variable holding one would compare false against every threshold and look
  like a number that is merely small.

  **`==` is unchanged and still textual.** `"5" == "5.0"` stays false: the
  equalities in existing skills compare ids and sentinels, and coercion would
  quietly change what they mean.

  There is deliberately no arithmetic (`a + b > c`, `len(x) > 0`): those are
  expressions, and expressions are the door to skills that cannot be read from
  top to bottom.

- **Changed**: a condition written with braces — `{{pod.restartCount}} > 5`, the
  form a model reaches for, since that is how substitution is written everywhere
  except conditions and `for_each.in` — is still refused, but the error now
  names the braces and prints the condition without them. Before, the author got
  a list of the allowed shapes and had to spot that their string differs from
  one of them by exactly two pairs of brackets.

- **Fixed**: a `for_each` over a JSON array of OBJECTS handed its body Go's map
  formatting (`map[name:api restartCount:12]`) instead of the object. Every
  field lookup inside the loop — `{{pod.name}}`, a condition on
  `pod.restartCount` — resolved to emptiness, and the model was shown a syntax
  belonging to the language the engine happens to be written in. Elements are
  now re-rendered as JSON; a string element is unchanged.

- **Changed (Go API)**: `CondVar` became `CondVars`, returning every variable a
  condition depends on rather than only the left one. A numeric comparison may
  name a variable on both sides, and a reader that saw only the left would leave
  a typo in a threshold unchecked — which is exactly what such a reader is
  usually looking for. Callers replace `name, ok := CondVar(c)` with
  `names, ok := CondVars(c)`.

## 2.2.4

Engine fixes; the format itself did not change. One of them **changes
behaviour** — read the first item before upgrading.

- **Fixed, and a behaviour change**: a model step could WIDEN an empty tool set.
  With `tools: []` on the flow and `tools: [something]` on the step, the step was
  handed `something` — the guard the flow was written for, undone by the step it
  was written against.

  The `call` path had always answered the opposite way (`allowServer`: "an empty
  flow set is NOT everything is allowed"), so one flow had two access policies
  depending on the kind of step. Now both hand out nothing.

  **If a skill of yours relies on naming tools per step while the flow declares
  none, those steps now receive an empty set.** The fix is to say on the flow
  which servers it may reach; a step still narrows from there.

- **Fixed**: a missing `Deps.Runner` panicked instead of failing. `call` and
  `delegate` had always reported a missing executor as an ordinary step failure,
  subject to `on_error`; an instruction step dereferenced nil and took the
  process with it — a stack trace where "you did not pass Deps.Runner" belonged.

- **Fixed**: `Flow.Validate` rewrote the description it was given. Validation
  needs the steps in the shape execution reads them — profiles folded in, a
  `save_as` beside a call moved into it — and it did that in place. Two
  consequences: a caller holding a parsed Flow saw the engine's working copy
  instead of the file they parsed, and two turns over one `*Flow` wrote to the
  same structs from two goroutines.

  Validation now works on a copy, and execution runs that copy. "Parse once, run
  many times" — including concurrently — is safe, which for a skill engine is
  the natural way to use it.

- **Documented, not changed**: everything in `Deps` may be called concurrently.
  Within one turn the branches of a `parallel` run at once; across turns one
  `Deps` usually serves the whole application. Ordinary clients already are safe
  for that — a hand-written double or a callback accumulating into a slice is
  where it gets forgotten.

## 2.2.3

An engine fix; the format itself did not change.

- **Fixed**: a branch of a `parallel` step did not inherit the assets, their
  resolver, cache and context, working memory, or the application's vocabulary.
  The sub-state was assembled by listing fields, and those six were not on the
  list.

  Nothing failed loudly. An unknown asset expands to an empty string by
  contract, so `{{asset:x}}` inside a branch quietly became "" and the tool call
  that needed it lost a required argument — with the error pointing at the
  argument rather than at the substitution. Working memory and the vocabulary
  went the same way: a step in a branch read a preview instead of the whole
  value, and `one_of` lost its tie-breaker.

  It stayed hidden because in a live catalogue of 29 skills no `call` step with
  an asset had ever sat inside a `parallel` branch.

  The branch state is now FORKED from the flow's and the few branch-local
  fields are reset explicitly, so a field added to the engine reaches branches
  by default. The list had the opposite default, and whoever adds a field is
  not thinking about `parallel`.

  The asset cache is shared with the branches rather than copied into them, so
  an asset three branches need is still fetched once — under a lock held across
  the resolve. CI now runs the race detector.

- **Documented, not changed**: `Deps.OnStep` and `Deps.OnStepStart` fire from
  the goroutine that ran the step, so inside a `parallel` they fire from several
  at once and a callback that appends to a slice needs its own lock. The engine
  does not serialise them on purpose — a lock there would hold up a branch for
  the duration of somebody else's telemetry write. Found by the race detector
  added above, in a test written the way an embedder would write it.

- **Documented, not changed**: `Outcome.Steps` stops at a `parallel` — the steps
  inside its branches are not in it, while `Outcome.Skipped` does include them.
  Branch steps reach `OnStep` as they happen, so nothing is lost; but the two
  fields disagree, and a reader who checks one and assumes the other loses an
  afternoon. Now stated on the type and pinned by a test.

## 2.2.2

An engine fix; the format itself did not change.

- **Fixed**: the working-memory handle was looked for ANYWHERE in a value, while
  the host writes it on the LAST line — the same place `trimHostNote` cuts. A
  value that quotes arbitrary content quotes everything, so a diff, a log or a
  user's message naming `[mem:…]` was read as a control marker.

  Two failures came out of that, and the second one is older than 2.2.1:

  - a toolless step was handed a value the engine believed to be an unreadable
    preview, and was marked degraded although the value was whole all along. It
    landed on exactly the repositories where this mechanism is implemented —
    that is, on any embedder reviewing its own code — and looked like a flake,
    because it depended on which chunk the quoting lines fell into;
  - with a real note present as well, the FIRST match won: `<var>.mem` ended up
    holding an id taken out of the data, so `{from: "{{var.mem}}"}` would hand a
    tool whatever that id happened to resolve to.

  A last line that genuinely begins with the marker stays ambiguous, and
  unavoidably so — the convention is a textual one. What is fixed is the far
  commoner half: a marker in the MIDDLE of quoted content is data again.

  The neighbouring parsers were checked and were already right: `trimHostNote`
  and `Vocabulary.TruncationNotes` read the last line, `ERROR:`/`DENIED:` are
  matched at the start of a value.

## 2.2.1

An engine fix; the format itself did not change, so nothing needs migrating and
no skill has to declare this version.

- **Fixed**: a step declared WITHOUT tools was substituted a large value the way
  a step with tools is — a fragment plus the host's note saying how to fetch the
  rest. It has nothing to fetch it with, and a model told to make a call it
  cannot make writes the call out as its answer. A live turn ended with the
  arguments of a memory call printed where a report was meant; the step was
  recorded `ok`, the counters were clean, and the user saw it first.

  Such a step is now substituted the WHOLE value, note stripped. The addressee
  of a substitution turned out to be three, not two: a script or a call argument
  needs the payload, a model that CAN fetch more needs the fragment and the
  note, and a model that cannot needs the whole thing.

  Size is deliberately not capped. Handing over a fragment above some ceiling
  needs the fragment to SAY it is one, and the library ships no words: it would
  have to keep the host's note (the failure above), strip it (a step reasoning
  over a fragment it cannot know is one), or invent wording in a language it
  does not know. A long prompt fails loudly; all three of those fail quietly.

  Where the whole value cannot be read at all — a handle with no `Deps.Memory`
  to resolve it — the step still runs on the fragment and is marked **degraded**
  with the variable named. It worked on part of the data, and nothing about its
  answer would have shown that.

## 2.2.0

One new condition form. A skill that does not use it behaves exactly as it did,
so nothing needs migrating; a skill that uses it must declare
`skill_engine_version: 2.2.0`, which is what stops an older engine from reading
it and refusing the condition it cannot parse.

- **Added**: `var contains a | b | c` and `var not contains a | b` alongside
  `==`, `!=` and `is [not] empty`. Any ONE alternative is enough; an alternative
  may contain spaces.

  Why. A classifier step whose whole job is "which of these words did the
  request name" already carries the mapping in its own text — the decision is
  deterministic, and the model is there only to apply it. Measured on ten live
  requests: the model at temperature 0 got 5 of 10, the same dictionary in a
  condition got 10 of 10, and three rewordings of the instruction did not move
  the ceiling. Every miss was one kind — falling back to a default and dropping
  what the request had NAMED. In one live catalogue 14 steps across 14 skills
  carry such a dictionary; each is a model call this replaces.

  Matching is case-insensitive across scripts, not only ASCII. A match must
  begin where a WORD STARTS — the default rather than an option, because the
  author will not think about it while a false match is nearly impossible to
  debug: the condition looks right. (Go's `\b` is ASCII-only and does not work
  on Cyrillic at all.) The END is deliberately free, so an alternative matches a
  word that starts with it: that is what lets a dictionary hold ROOTS — «заказ»
  finds «заказы», «заказа», «заказу» — and a dictionary of roots is why the format
  needs no stemming, which would be a guess about a language the engine does not
  know. The cost is that a too-short root collides, and the linter's W18 warns
  when one alternative is already covered by a shorter one.

  There are deliberately no regular expressions in a condition: they would make
  skills unreadable and open the door to catastrophic backtracking.

- **Changed**: a condition now reads the WHOLE value of its variable, with the
  host's note stripped, the way a tool argument and a loop's collection already
  did. A large result lives in a variable as a preview plus a handle, and a
  condition reading the preview answered about the first few hundred bytes while
  looking exactly as if it had answered about the value. No skill that fits in a
  preview changes behaviour.

## 2.1.0

Two optional fields. A skill that does not mention them behaves exactly as it
did, so nothing needs migrating; a skill that uses one must declare
`skill_engine_version: 2.1.0`, which is what stops an older engine from reading
it and quietly ignoring the field.

- **Added**: `profiles` on a workflow and `profile` on a step — a named set of
  generation parameters (`model`, `sampling`, `tools`, `max_calls`,
  `max_tool_errors`, `on_error`). A field written on the step wins; `sampling`
  is replaced whole rather than merged key by key. Measured on a live catalogue
  of 168 steps: the instructions are nearly all unique while 32 steps carry one
  identical envelope and 46 another — what repeats is the configuration, not the
  step. `tools: []` in a profile means an empty set, not "unset", so the guard
  "do not go to that source" survives being shared.

  Folded into the step before validation, so nothing downstream knows profiles
  exist — including the `response_schema` rule, which is satisfied by a model
  inherited from a profile and still refuses a step that ends up without one.

- **Added**: `on_empty` on a step and inside `call` — what an empty result
  *means*: `continue` (the default, and the old behaviour), `fail` (the step
  counts as failed, then `on_error` decides), `retry` (`on_empty_retries` more
  attempts, 1..5, default 1; still empty afterwards is treated as `fail`), and
  `use` with `on_empty_value`. Emptiness is an empty string after trimming,
  judged on the value the step *would store*: with `one_of` an ambiguous answer
  produces text and stores nothing, and it is the stored value that flows on.

  `retry` is refused on a `call` step — a call cannot be repeated, and a
  repeated call with a side effect is a second ticket, a second merge request,
  a second e-mail.

## 2.0.0

Incompatible. Skills on format 1.x need migrating; `Migrate` does all of it.

- **Breaking**: an asset's `lang` is gone. Everything that only makes sense for
  a specific kind moved into the open `params` map:
  `lang: python` → `params: {lang: python}`. Why: kinds are the application's
  vocabulary, and a field per kind would mean the format grows with every
  foreign kind — while sitting empty on every other asset. That is exactly how
  `lang` lived, needed by one kind out of four.
- **Breaking**: `CheckEngineVersion` now also rejects skills of a *previous*
  major (before, only ones "from the future" were rejected), including skills
  that declare no version at all — those read as 1.0.0.
- **Breaking**: `Flow.Validate` now refuses a step that carries a
  `response_schema` without a `model`, so a skill breaking the pair fails before
  the first generation instead of running. The rule is not new — the schema has
  demanded it from the start — but it was enforced only for embedders who run a
  JSON-schema validator, and the engine cannot run one (it would be a
  dependency). Left unpaired, the decoding grammar is dropped on some paths to a
  model and a "structured answer" degenerates into "the model usually answers
  JSON": parsed by luck, failing without a trace. Add the `model` the step was
  already supposed to name.
- **Added**: `mode: workflow | playbook` — which of the two descriptions to run
  when a skill has both. Without the field the default applies: "there is a
  `workflow` → follow it". An explicit mode with an empty half refuses rather
  than falling back to the other one — a silent fallback would give a clean run
  over the description that was *not* selected. The whole table is implemented
  by `ResolveMode`; the two statically checkable cases are in the schema.

Major 1.x was never released publicly (there are no tags), so 1.1.0 with the
`mode` field is not counted as a separate version — the field went straight into
2.0.0.
