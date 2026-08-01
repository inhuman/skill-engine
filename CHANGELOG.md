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
