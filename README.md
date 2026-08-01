# skill-engine

**English** · [Русский](README.ru.md)

An engine for declarative programs for an LLM agent: a skill is described in
**steps**, and control over the turn belongs to the code, not to the model.
Steps are not the only form: a skill with only a `playbook` (a free-form
instruction) is a full skill too (see "A prompt works as well").

**No dependencies** — production code runs on the standard library alone: the
engine is embedded into someone else's application, and every dependency here
would become a dependency of the embedder. YAML parsing is passed in as a
parameter (the `Unmarshal` type), version comparison is implemented in place.
The boundary is held by a guard test, `imports_test.go`, test imports included.

## Why

A restriction written in words is a request: "do NOT call retract without
confirmation", "run the check EXACTLY once". It is followed exactly as far as
the model read before it started acting. In steps the same thing is expressed
structurally: in the unconfirmed branch the `retract` call **is not there**, a
`call` step cannot be repeated, a branch that does not apply does not run.

Measurements on live skills (tool calls / seconds, before → after): plotting a
chart from tickets 18/95 → **2/6**, listing a cluster's pods 7/33 → **2/5**,
searching the wiki 9/29 → **5/11**.

## A prompt works as well

Starting with structure is not required. A skill has two ways to describe its
turn:

- `playbook` — a free-form instruction: what to do and what to look at;
- `workflow` — steps (`steps`, `tools`, `vars`, `assets`), i.e. everything
  below in this file.

The usual path is to write it as a prompt, debug it on live requests, and move
into steps whatever is worth it: structure costs time, and there is no reason
to pay for it before you know WHAT to structure. The measurements above are
about that move.

While the move is under way both descriptions can sit side by side, with the
`mode` field switching between them so their outcomes can be compared on live
requests — instead of deleting half the work just to check:

| `mode` | `workflow` | `playbook` | what runs |
|---|---|---|---|
| unset | present | present | `workflow` — structure outranks prose |
| unset | present | — | `workflow` |
| unset | — | present | `playbook` |
| unset | — | — | error: the skill describes no turn |
| `workflow` | present | any | `workflow` |
| `workflow` | — | present | **error**: the mode is set, there are no steps |
| `playbook` | any | present | `playbook` |
| `playbook` | present | — | **error**: the mode is set, there is no text |

An empty half under an explicit mode is a refusal, not a fallback to the other
one: switch the mode to `playbook`, forget to write the text, and you would
otherwise get a clean run over the old steps and the conclusion "in playbook
mode it works the same" — from a turn the `playbook` never took part in. The
first two errors are caught statically by the schema (`if/then` on `mode`); the
whole table is implemented by `ResolveMode`.

The engine reads only `workflow` — `Flow` has no `playbook` field. A skill
without steps is run by the embedding application its ordinary way: it is a
prompt, and the engine has nothing to do there. `ResolveMode` lives here
because "which of the two descriptions is in effect" is format semantics: were
it different in every host, skill portability would end silently.

## Example

```yaml
tools: [staging, exec]
steps:
  - name: understand                  # parse the request into fields
    instruction: |
      Request: {{input}}
      cluster — which cluster is named; namespace — the namespace name.
    tools: []                         # this step needs no tools
    model: vllm/gemma-4-e4b
    response_schema:
      type: object
      properties:
        cluster: {enum: [staging, sandbox]}
        namespace: {type: string}
      required: [cluster, namespace]   # see "Format pitfalls"
    save_as: req

  - name: fetch_pods                  # a call WITHOUT generation
    call:
      tool: kubectl_get
      args: {namespace: "{{req.namespace}}", resourceType: pod}
    on_server: "{{req.cluster}}"
    save_as: pods

  - name: report                      # a step without save_as writes the turn's answer
    instruction: |
      Pods: {{pods}}
      Answer: pod name → status.
    tools: []
```

## Step kinds

| step | what it does |
|---|---|
| `instruction` | generation by the model; `tools` sets the RADIUS — an empty list means "no tools" |
| `call` | a tool call without generation; arguments in YAML |
| `set` | assigning a variable |
| `switch` / `if` | branching on a variable's value |
| `for_each` | a loop over a collection, `collect` gathers the body's results |
| `parallel` | parallel branches; `<collect>.skipped` — those that did not run because of `when` |
| `delegate` | delegating to another skill (the application decides how to execute it) |
| `exit` | "matched by mistake" — the turn returns to its ordinary path |

## Variables

- `save_as` puts a step's result into a variable; **a step without `save_as`
  writes into `answer`** — that is where the application takes the turn's
  answer from. An empty `answer` = the program produced no answer.
- `<name>.mem` — the working-memory handle of a result, ALWAYS, not only for
  large ones: `args: {stdin: {from: "{{tickets.mem}}"}}` sends the data past
  the model's context.
- `{{asset:name}}` substitutes an asset into TEXT (it passes through the
  context), `{from: "asset:name"}` — by REFERENCE (it does not).
- `<collect>.skipped` — branches skipped because of `when`. Without it the
  answering step cannot tell "the source answered nothing" from "we never went
  to the source".

## The contract with the application

```go
out, outcome, err := skillengine.ExecuteWith(ctx, flow, skillengine.Deps{
    Runner:   …, // executes an instruction step (generation)
    Caller:   …, // executes a call step (a tool)
    Delegate: …, // executes a delegate step
    Assets:   …, // resolves asset content                    (optional)
    Memory:   …, // returns a full result by its .mem handle   (optional)
    OnStep:   …, // a step's trace RIGHT AFTER it, not in bulk (optional)
}, vars)
```

- `out` — the variables **produced by the steps**; the `vars` passed in do not
  end up in the result. Otherwise a flow that did not fill in the answer hands
  the caller its own input — live case: a user got a transcript of their own
  messages in chat instead of an answer.
- `Outcome.Steps` — the trace of every step (name, kind, outcome, reason,
  duration, number of calls and failures);
- `Outcome.Skipped` — steps not executed because of `when`;
- `Outcome.AnsweredBy` — `instruction` or `call`: what wrote the answer. Needed
  so that post-processing does not rewrite a script's deterministic output.

The engine logs nothing, persists nothing and goes nowhere: the input and the
steps' output are the caller's data. Everything visible from outside is handed
over as a structure (`Outcome`) and through callbacks (`OnStepStart` — before a
step, for showing work to a human; `OnStep` — right after). Turning that into
telemetry is the embedding application's job.

The format's schema is `skill.schema.yaml` — the source of truth, embedded as
`SchemaYAML`, with `SchemaSummary` giving the compact version to hand a model.
`SchemaRU` / `SchemaSummaryRU` are the same in Russian: also embedded, so
`go mod vendor` carries them to whoever shows the schema to a skill author. A
test keeps the two structurally identical, so only the prose differs, and
validation always goes against the English one.

The format version is in `version.go`; `CheckEngineVersion` rejects both a
description from the future and one of a foreign major: the latter would parse
without a single complaint, silently losing fields the structs no longer have.
Since skills live in a user's storage and are not updated with a deploy, the
edits that a major needs ship with the code that makes them:

```go
out, changed, err := skillengine.Migrate(raw)
```

`Migrate` edits the file as text — comments, key order and block scalars
survive — and does not validate the result; parse and validate it as usual
afterwards. Format changes and what each migration does are in `CHANGELOG.md`.

## Invariants paid for with live failures

- **A failure must be loud.** `degraded` is set on a step with no text, on a
  fork where no branch ran, on a `switch` with no match and an empty `default`,
  on a loop with failed iterations, on a truncated answer. A silent failure
  here looks like success: the turn answers with an internal variable, and that
  reads as a finished answer.
- **A mechanism added to the model's path must appear on the `call` path too.**
  Nine misses in a row, each found by a live failure: empty arguments, `{from:}`
  references, delivery, retries, request normalisation, provenance, argv
  repair, builtin tools, cross-turn memory.
- **Knowledge is expensive in a step WITH TOOLS**: an asset rides along into
  EVERY generation of the react loop. The cure is splitting it into "decide"
  (knowledge, no tools) and "do" (tools, no knowledge).

## Format pitfalls

- **`required` is the only lever.** A `strict` schema enforces only what is
  listed: a field in `properties` but not in `required` may legitimately not be
  sent by the model. Live measurement: `confidence` did not arrive ONCE out of
  26 findings, while 16 of them wrote the number in words inside the text.
- **A string field needs `maxLength`.** Otherwise the model writes until the
  token ceiling and breaks off mid-line, taking the whole document with it. The
  grammar holds the limit: `maxLength: 600` → exactly 600 characters and valid
  JSON.
- **`for_each.in` takes a variable NAME, not a template.** `in: "{{parts}}"`
  yields zero iterations and reports success (the engine now rejects that).
- **The exec envelope is not the payload.** `{{findings}}` is
  `{"exit_code":…,"stdout":"…"}`; a loop and the arguments need `.stdout`.

The joints between steps are where programs break, and static checks catch them
more cheaply than a run does. `Flow.Validate` is called before execution and
rejects what used to be reported as success; every new such class is closed off
by a check in validation rather than by a paragraph here. Running it over
descriptions before execution is worth it too — in CI, when a skill is written.

## Tests

`example_flow_test.go` — runnable examples of the format, a good first entry
point. `examples_test.go` parses every file from `examples/` with the engine: an
example that stopped parsing is worse than a missing one — it teaches the wrong
thing.
