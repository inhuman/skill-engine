# Quickstart

**English** · [Русский](QUICKSTART.ru.md)

Fifteen minutes, at the end of which you will have run the **same skill both
ways** — as a prompt and as steps — on the same request, and seen the difference
in what it cost and in what it did.

Written to be followed literally, by a person or by a model: every step has the
command, the file, and the output to expect.

---

## What you are going to see

One file describes a turn twice: as prose (`playbook`) and as steps
(`workflow`). Running it both ways on `подбери десерт и напиток`:

```
=== cost (playbook) ===
  generations: 1
  tokens:      214 prompt + 13 completion = 227

=== cost (workflow) ===
  generations: 1
  tokens:      89 prompt + 13 completion = 102
```

And on a request that names no section at all:

```
-mode playbook   → the model answers something. It was asked not to invent, and
                   whether it obeys is a probability.
-mode workflow   → the skill stepped aside: exit: the request names no section
                   of the menu
```

Two different things happened there, and only one of them is about tokens.

---

## Step 0 — what you need

- **Go 1.26+**
- **an OpenAI-compatible endpoint.** Anything that speaks
  `POST /v1/chat/completions`: vLLM, Ollama, LM Studio, LocalAI, or a hosted
  API. The fastest local one:

  ```
  ollama serve
  ollama pull qwen3:8b
  ```

  which gives you `http://localhost:11434/v1` and needs no key.

You can do **Steps 1–5 without any endpoint at all** — the example's tests run
the whole thing against a stub. The numbers above come from exactly that stub,
which charges by prompt length the way a real endpoint does.

---

## Step 1 — get the repository and run the tests

```
git clone https://github.com/inhuman/skill-engine
cd skill-engine/examples/simple-llm-app
go test ./...
```

Expected:

```
ok  	github.com/inhuman/skill-engine/examples/simple-llm-app
```

That run just executed a skill end to end, twice, and compared the two. If it
passes, everything below will work.

---

## Step 2 — point the example at your model

```
export OPENAI_BASE_URL=http://localhost:11434/v1
export OPENAI_API_KEY=ollama          # any non-empty string for a local server
export OPENAI_MODEL=qwen3:8b
```

---

## Step 3 — run the skill as PROSE

```
go run . -skill ../skills/menu.yaml -mode playbook -input "подбери десерт и напиток"
```

The whole task goes to the model in one prompt: here are the sections, here are
the words people call them by, decide which were named, look them up, answer.

Write down the two numbers it prints under `=== cost (playbook) ===`.

---

## Step 4 — run the SAME FILE as steps

```
go run . -skill ../skills/menu.yaml -mode workflow -input "подбери десерт и напиток"
```

One word changed on the command line. Same file, same model, same request.

```
→ nothing_named (exit)
→ pick_dessert (call)
  call recipes:search map[query:подбери десерт и напиток section:dessert]
→ pick_drink (call)
  call recipes:search map[query:подбери десерт и напиток section:drink]
→ pick_main (call)
→ answer (instruction)

=== steps ===
  nothing_named    exit        skipped  calls=0  condition … is false
  pick_dessert     call        ok       calls=1
  pick_drink       call        ok       calls=1
  pick_main        call        skipped  calls=0  condition input contains горяч | второе | main course is false
  answer           instruction ok       calls=0

=== cost (workflow) ===
  generations: 1
  tokens:      89 prompt + 13 completion = 102
```

Compare with Step 3.

---

## Step 5 — where the difference came from

Open [`examples/skills/menu.yaml`](examples/skills/menu.yaml) next to the output
above. Four things did the work.

**1. The dictionary never reached the model.**

```yaml
- name: pick_dessert
  when: "input contains десерт | сладк | dessert"
```

In the prose half those synonyms are in the prompt, and applying them is a
generation the model can get wrong. Here they are a condition: the words are
matched in code, before anything is sent anywhere. That is most of the token
difference — and all of the accuracy difference.

**2. Two steps ran without a model at all.**

```yaml
  call:
    tool: "recipes:search"
    args: {section: dessert, query: "{{input}}"}
```

A `call` step is a tool invocation with the arguments already known. In prose
the same work is: one generation to decide to call, the call, another to read
the result back. Here it is the call.

**3. The answering step was handed no tools.**

```yaml
- name: answer
  instruction: |
    …
  tools: []
```

An empty list is not "no preference" — the step physically cannot call
anything. In prose, "do not go looking further" is a request; here there is
nothing to go with.

**4. What did not run is visible.**

```
skipped: nothing_named, pick_main
```

"We did not look there" and "we looked and it was empty" are different answers.
The trace keeps them apart, so the answering step cannot report one as the
other.

---

## Step 6 — make it decide something it cannot invent

```
go run . -skill ../skills/menu.yaml -mode workflow -input "что посоветуешь"
go run . -skill ../skills/menu.yaml -mode playbook -input "что посоветуешь"
```

Steps:

```
→ nothing_named (exit)
the skill stepped aside: skill-engine: exit: the request names no section of the menu
```

Prose: the model answers. It was told not to invent, and that instruction is
followed as far as it read before it started writing.

This is the property the format is for, and it is not about cost: **in steps the
impossible is impossible, not discouraged.** The branch that would have searched
does not exist on that path.

---

## Step 7 — write your own skill

The smallest useful file. Save it as `my-skill.yaml`:

```yaml
skill_engine_version: "2.2.0"
skill_version: "1.0.0"
name: my-skill
description: What this skill is FOR — and what it is NOT for.
trigger_examples:
  - "a phrasing a user would actually type"

workflow:
  steps:
    # A step with no tools cannot go anywhere. It thinks, and that is all.
    - name: answer
      instruction: |
        The request: {{input}}

        Answer it in two sentences.
      tools: []
```

```
go run . -skill ./my-skill.yaml -input "привет"
```

Then grow it in this order — each of these is one line in the file:

| you want | you add |
|---|---|
| branch on the words of the request | `when: "input contains word \| synonym"` |
| call a tool with known arguments | a `call:` step |
| loop over what a step returned | `for_each: {in: var, as: item, collect: out}` |
| stop when it is not your case | `exit: {reason: "…"}` |
| a structured answer to branch on | `response_schema:` **plus** `model:` |
| hand the work to another skill | `delegate: {skill: other, task: "…"}` |

The full field list with the reason each one exists is
[`skill.schema.yaml`](skill.schema.yaml); more shapes are in
[`examples/skills/`](examples/skills/).

---

## Step 8 — check a skill before you run it

Two levels, and they answer different questions.

```go
// Will it run at all?
if err := skill.Validate(); err != nil { … }
```

```go
// Will it run WELL? 30 rules for the defects that stay quiet.
rep, err := lint.Lint(raw, facts, lint.Options{
    Unmarshal:  yaml.Unmarshal,
    EmptyWords: []string{"empty", "пусто"},
    HostVars:   []string{"input"},
})
fmt.Println(rep.Text())
```

The linter catches things a run would not: a loop collecting into a variable
nobody writes, a typo in a variable name that resolves to an empty string, a
required field the instruction beside it allows to be empty. See
[`lint/README.md`](lint/README.md).

---

## Step 9 — embed it in your application

The whole contract is one struct. From
[`examples/simple-llm-app/main.go`](examples/simple-llm-app/main.go):

```go
skill, err := se.ParseSkill(raw, yaml.Unmarshal)   // the whole file
if err := skill.Validate(); err != nil { … }

mode, err := skill.ResolveMode()                   // steps or prose?
if mode == se.ModePlaybook {
    // the engine takes no part: run skill.Playbook as your own prompt
}

vars, outcome, err := se.ExecuteWith(ctx, skill.Workflow, se.Deps{
    Runner: yourModel,      // executes an instruction step
    Caller: yourTools,      // executes a call step
    Assets: yourResolver,   // resolves an asset's content
    Memory: yourMemory,     // returns a large result by its handle
    Vocabulary: se.Vocabulary{
        DecisionMarkers: []string{"Result:", "Ответ:"},
    },
    OnStepStart: func(name, kind string) { … },
}, map[string]string{"input": userText})
```

`vars[se.AnswerVar]` is the turn's answer. `outcome.Steps` is the trace you saw
above — the engine logs nothing and stores nothing, so that struct is the whole
of its observability.

Two working applications to copy from:

- [`examples/simple-llm-app`](examples/simple-llm-app/) — `net/http` and nothing
  else;
- [`examples/eino-llm-app`](examples/eino-llm-app/) — the model reached through a
  framework, with the whole framework-shaped part in one forty-line adapter.

---

## Honest notes about the numbers above

- They come from a **stub** that charges by prompt length, so they show the
  SHAPE of the difference rather than your model's bill. Run Steps 3–4 against
  a real endpoint for real numbers.
- With a real endpoint the prose half also spends **extra generations**: it has
  to decide to call a tool, call it, and read the result back. The stub cannot
  call tools, so it does that work in one generation and the gap in the table
  above is understated, not overstated.
- The real numbers are in [README.md](README.md): a live installation's event
  log, 5 280 turns over five weeks, 23 skills — 20 significantly cheaper, 1
  significantly more expensive, 2 unchanged. That comparison is
  **observational**: the periods are split by a date rather than randomised, and
  other things changed in those same days.
- In that same measurement one skill got **more** expensive: a median of
  **6 → 10 generations per turn**. Steps are not automatically cheaper — the
  README lists the known ways a rewrite costs more, and every skill is worth
  measuring on its own afterwards.

---

## Where to go next

| | |
|---|---|
| [README.md](README.md) | what the engine is, what it is NOT, and what you can rely on |
| [`examples/`](examples/) | the format and two applications that embed it |
| [`skill.schema.yaml`](skill.schema.yaml) | every field, and the failure that paid for it |
| [CHANGELOG.md](CHANGELOG.md) | what changed in the format, and what a migration does |
| [`lint/README.md`](lint/README.md) | the 30 rules and what each one catches |
