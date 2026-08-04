# Examples

Two things live here: skills, and applications that run them.

| | |
|---|---|
| [`skills/`](skills/) | the format itself — working skills from different areas, plus a vocabulary of the values the schema deliberately leaves open |
| [`simple-llm-app/`](simple-llm-app/) | the engine embedded in ~200 lines: an OpenAI-compatible endpoint over `net/http`, no dependencies beyond the engine and a YAML parser |
| [`eino-llm-app/`](eino-llm-app/) | the same application with the model reached through [eino](https://github.com/cloudwego/eino) — the whole framework-shaped part is one forty-line adapter |

## Why each application is its own module

Both apps have their own `go.mod`, and that is the point rather than tidiness.

The engine's promise is that embedding it adds no dependencies: production code
is stdlib only, and a guard test fails the build the moment that stops being
true. An example that pulls in a framework would break exactly that promise —
unless it is a separate module, which is what `go.mod` next to it makes it. The
engine's `go.mod` never learns that eino exists.

Two guards hold this in place, both in `imports_test.go` upstairs: one skips
nested modules while checking that the engine imports nothing, the other checks
that a directory with Go code in it still HAS a `go.mod`. Delete one of those
files and the first guard immediately reports eino as a dependency of the
engine — which is what it is at that moment.

CI runs `go vet` and `go test` inside each example module separately, because
`go test ./...` at the top never descends into them. An example that does not
build teaches the format wrong.

## Running one

```
cd simple-llm-app
export OPENAI_BASE_URL=http://localhost:8000/v1   # vLLM, Ollama, LM Studio, …
export OPENAI_API_KEY=…
export OPENAI_MODEL=…

go run . -skill ../skills/menu.yaml -input "подбери десерт и напиток"
```

Both applications print the answer and then the trace — which step ran, which
was skipped and why, how many tool calls each made. That trace is the engine's
whole observability contract: it logs nothing, stores nothing and reaches
nowhere on its own.

Neither needs a key to be **tested**: each has a test that runs the whole
application against a stub model, so "it works" is something you can check
rather than something this file claims.
