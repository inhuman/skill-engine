## What

<!-- One paragraph: what does this PR change? -->

## Why

<!-- The motivation. Link to the issue if one exists. -->

Closes #

## How

<!-- Brief implementation notes. What did you change architecturally? Any tricky bits the reviewer should pay attention to? -->

## Testing

- [ ] `go vet ./...` clean
- [ ] `go test ./... -count=1` all green
- [ ] New tests added for new behaviour, and they aim at the live path
      (a test propping up a dead duplicate is worse than no test)
- [ ] If the format changed: `examples/` updated and still parse

## No dependencies

<!-- The engine is embedded into someone else's application: every dependency
     here becomes theirs, with their versions and their conflicts. -->

- [ ] Production code still imports stdlib only (`imports_test.go` green)
- [ ] No new test dependency outside `allowedTestDeps` (or: the list change is
      argued in the PR body)

## Format & API compatibility

<!-- Skills live in a user's storage and are NOT updated with a deploy: changing
     the meaning of a field silently changes the behaviour of skills already
     written. -->

- [ ] Does not change the meaning of an existing field, or bumps `EngineVersion`
      accordingly (new optional field = MINOR, incompatible = MAJOR)
- [ ] `CHANGELOG.md` updated with what breaks and how to migrate
- [ ] If a skill file needs edits: `Migrate` does them, and is idempotent on
      files already on the current major
- [ ] Go API: no breaking change to `Deps` / `ExecuteWith` / `Outcome` without a
      MAJOR bump (a new executor goes in as an optional `Deps` field)

## Schema & docs

- [ ] `skill.schema.yaml` updated (the source of truth, embedded)
- [ ] `skill.schema.ru.yaml` updated to match — the sync test only guards the
      structure, the prose is on you
- [ ] `README.md` and `README.ru.md` both updated if user-visible behaviour changed

## Invariants

<!-- These are the classes that have already cost live failures. -->

- [ ] A failure is LOUD: whatever cannot do its job is marked `degraded` in
      `Outcome`, never left looking like success
- [ ] A mechanism added to the model's path also exists on the `call` path
      (and the other way round)
- [ ] The engine still logs nothing, persists nothing and reaches nowhere:
      observability leaves through `Outcome` and the callbacks
- [ ] A new failure class of the format is closed by a check in `Flow.Validate`,
      not by a paragraph in the README

## Checklist

- [ ] Godoc added/updated for new exported identifiers
- [ ] No secrets / tokens in the diff
