---
name: Bug report
about: Something doesn't work as documented
title: "bug: "
labels: bug
---

## Summary

<!-- One sentence: what's broken? -->

## Environment

- skill-engine version: <!-- module version (e.g. v2.0.0) or "main@abc1234" -->
- `EngineVersion` / the skill's `skill_engine_version`: <!-- e.g. engine 2.0.0, skill 2.0.0 -->
- Go version: <!-- go version output -->
- Which executors you wire into `Deps`: <!-- Runner / Caller / Delegate / Assets / Memory -->

## The skill

The smallest `workflow` that reproduces it (strip anything private):

```yaml
tools: [srv]
steps:
  - name: parse
    instruction: "..."
    tools: []
    save_as: req
```

## Reproduction

How you call the engine — a `Runner`/`ToolCaller` stub is usually enough:

```go
out, outcome, err := skillengine.ExecuteWith(ctx, &f, skillengine.Deps{
    Runner: skillengine.RunnerFunc(func(_ context.Context, req skillengine.StepRequest) (skillengine.Result, error) {
        return skillengine.Result{Text: "..."}, nil
    }),
}, map[string]string{"input": "..."})
```

## Expected behaviour

<!-- What you thought should happen. -->

## Actual behaviour

<!-- What happened instead. -->

## What the trace says

<!-- Almost every diagnosis starts here: Outcome.Steps carries each step's kind,
     outcome (ok / skipped / denied / error / degraded), reason, calls and
     duration; Outcome.Skipped lists steps dropped by `when`; AnsweredBy says
     what wrote the answer. Paste the relevant rows, not the payloads. -->

```
step=parse kind=instruction outcome=degraded reason="step produced no text" calls=0
```

## Additional context

<!-- Anything else: does the skill validate (`Flow.Validate`)? Does it validate
     against skill.schema.yaml with a JSON-schema validator? Did it work on an
     earlier format major? -->
