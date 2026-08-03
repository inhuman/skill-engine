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
