# eino-llm-app

The same application as `../simple-llm-app`, with the model reached through
[eino](https://github.com/cloudwego/eino).

```
export OPENAI_BASE_URL=http://localhost:8000/v1
export OPENAI_API_KEY=…
export OPENAI_MODEL=…

go run . -skill ../skills/menu.yaml -input "подбери десерт и напиток"
```

## The whole point is `runner.go`

Forty lines, and everything eino-shaped in this example lives in them. The
adapter takes `model.BaseChatModel` — the interface, not a vendor — so eino's
OpenAI, Ark, Ollama or Qwen components drop in without touching it, and so does
a stub in a test.

The direction is what matters: **the engine does not know eino, and eino does
not know the engine.** The application owns the seam between them. That is what
makes the engine embeddable in an application built on some other framework
tomorrow — and it is why this directory has its own `go.mod`.

```
examples/eino-llm-app/go.mod   →  requires eino
skill-engine/go.mod            →  has never heard of it
```

A guard test upstairs (`imports_test.go`) enforces both halves: it skips nested
modules when checking that the engine imports nothing, and it fails if a
directory with Go code in it loses its `go.mod`. Delete this one and eino is
reported as a dependency of the engine on the next run — which, at that moment,
is exactly what it would be.

## What the adapter carries across

**Declarations.** A step's `model:` and `sampling:` become `model.WithModel`,
`WithTemperature`, `WithTopP`, `WithMaxTokens`. A field the skill sets and the
executor drops is decoration — the author writes `temperature: 0` on a
classifier for a reason, and nothing downstream would tell them it never
arrived. There is a test for exactly this.

**The step's radius.** An empty tool set is not "no preference": it is the
skill's guard, and the adapter hands the model nothing. Binding tools "just in
case" is how a prohibition quietly stops being one.

**What the executor knows and the engine cannot derive.** A generation stopped
at the token ceiling comes back as `Result.Truncated` with a reason, and the
engine marks the step degraded on the strength of it. A truncated answer and a
step that simply had nothing to say are indistinguishable from the text alone.

## Testing it

```
go test ./...
```

The tests run the application end to end against a stub chat model — no network,
no key — because the adapter takes eino's interface rather than its client. They
check the same flow as the simple app, plus that the skill's declarations really
reach the model as options.

## The `replace` in go.mod

```
replace github.com/inhuman/skill-engine => ../..
```

It points at the engine in this repository, so CI checks the example against the
code as it is now rather than against the last release — an API break is caught
the day it happens, not a version later. **Delete that line when you copy this
example into your own project**; the `require` above it is what you actually
want.
