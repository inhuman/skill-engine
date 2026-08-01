---
name: Feature request
about: Suggest a new step kind, field, or behaviour change
title: "feat: "
labels: enhancement
---

## Problem

<!-- What you're trying to express in a skill that the format can't express today,
     or expresses awkwardly. A concrete skill you tried to write, not an abstract
     wishlist. -->

## Proposed solution

<!-- What you'd like the engine to do. A new step kind? A new field? A different
     default? Another executor in Deps? -->

## Alternatives considered

<!-- Workarounds you've tried, or simpler shapes you ruled out and why. Note that
     three similar lines beat a premature abstraction here. -->

## Constraints / non-goals

<!-- What this should NOT do. Note the invariants that are not up for negotiation:
     production code has no dependencies; the engine logs nothing and reaches
     nowhere; inside a `workflow` the decision does not go back to the model;
     a failure is loud rather than silent. -->

## Example usage

<!-- A snippet showing how it would be written in a skill, even if pseudo. -->

```yaml
steps:
  - name: example
    your_new_field: ...
```

```go
skillengine.ExecuteWith(ctx, &f, skillengine.Deps{ /* ... */ }, vars)
```

## Impact on existing skills

<!-- Skills live in a user's storage and are not updated with a deploy. Does this
     change the meaning of an existing field? Does it need a MINOR (new optional
     field) or a MAJOR (incompatible) bump of EngineVersion? If skill files need
     edits, can Migrate make them mechanically? -->

## Both paths

<!-- If this adds a mechanism to the model's path (instruction steps), what is the
     equivalent on the `call` path — and the other way round? Nine live failures
     in a row came from adding to one and forgetting the other. -->
