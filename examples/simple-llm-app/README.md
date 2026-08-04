# simple-llm-app

The engine embedded in an application, with nothing between it and the model but
`net/http`.

```
export OPENAI_BASE_URL=http://localhost:8000/v1   # vLLM, Ollama, LM Studio, …
export OPENAI_API_KEY=…
export OPENAI_MODEL=…

go run . -skill ../skills/menu.yaml -input "подбери десерт и напиток"
```

## What it shows

**Loading a skill.** `ParseSkill` reads the whole file — header and description
alike — and `Validate` is a separate call on purpose: "this is not YAML" and
"this YAML says something wrong" are different answers, and an application
reporting on a file has to tell them apart.

**Both halves of the format.** `ResolveMode` says which description runs. A
skill may carry a `playbook` instead of steps, and then the engine takes no part
at all — the application runs the prompt itself. Handling that case is what makes
an embedder complete rather than half-written.

**The four seams.** `Deps` is the entire contract:

| | |
|---|---|
| `Runner` | the ONE place the engine touches a model. The step arrives resolved — variables substituted, assets inlined, tools narrowed — and all that is left is to generate |
| `Caller` | a `call` step: a tool invocation with no model involved. Here it is a table so the example runs offline; in your application it is your MCP client |
| `Assets` | payloads a skill declares. Even `source: inline` comes through here — an asset is the application's to fetch, cache and police |
| `Memory` | a large result by the handle the host appended to it |

**Vocabulary.** The engine ships no words of its own. What your model writes
before naming a choice, and how your host marks a shortened result, are declared
here — an agent about a kitchen and one about a car fleet share the format, not
a language.

**What the skill declared has to arrive.** A step's `model:`, its `sampling:`,
its `response_schema:` are forwarded to the endpoint. An executor that drops
them turns every one of those fields into decoration: the skill declares, and
nothing happens, and nothing says so.

**Failing properly.** A tool that does not exist returns an error rather than an
apology — the skill's `on_error` decides what happens next, and it can only
decide if it is told. A skill leaving through `exit` is not a failure: it decided
the request was not its case.

## Testing it

```
go test ./...
```

The test runs the whole application against a stub OpenAI-compatible server, on
a skill shipped in `../skills`. No key, no network. It checks that the
conditions picked the sections the request named, that the `call` steps ran
without a model, that a section nobody asked for was skipped **and still
reported as skipped** — "we did not go there" and "it came back empty" are
different answers — and that every shipped skill still loads through this path.

## The `replace` in go.mod

```
replace github.com/inhuman/skill-engine => ../..
```

It points at the engine in this repository, so CI checks the example against the
code as it is now rather than against the last release — an API break is caught
the day it happens, not a version later. **Delete that line when you copy this
example into your own project**; the `require` above it is what you actually
want.
