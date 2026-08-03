# skill-lint

Checks a skill against the rules of the format — the ones the engine cannot
enforce at run time without having already burned the turn.

Every rule here was paid for by a broken turn, and each is about a defect that
stays **quiet**: a loop that collects into a variable nobody writes runs all its
iterations and gathers nothing; a typo in a variable's name resolves to an empty
string; a required field the instruction allows to be empty sends the model into
whitespace up to the token ceiling. None of it raises an error. All of it
produces an answer that looks fine.

```go
import (
    "gopkg.in/yaml.v3"
    "github.com/inhuman/skill-engine/lint"
)

rep, err := lint.Lint(raw, facts, lint.Options{Unmarshal: yaml.Unmarshal})
if err != nil {
    return err // the check could not run — not a verdict on the skill
}
fmt.Println(rep.Text())
if rep.HasErrors() {
    // your policy decides what that means
}
```

`err` is an infrastructure failure. "The skill is bad" is always findings in the
report.

## The line with `Flow.Validate`

`Validate` refuses what **cannot run**. The linter reports what **runs badly**.
Do not move advice into `Validate`: a rule promoted there starts breaking skills
that are already written and already working.

The two are used together — the linter runs `Validate` itself (rule W1) and
reads the description in the shape execution will see it, profiles folded in.

## Severity is ours, the gate is yours

The library says "this is a defect of the format". Whether that stops a save,
fails a build, or is merely shown to the author is the embedder's policy — which
is why there are no profiles here.

## Facts: the rule knows *what* to compare, you say *what with*

Which servers are up, which tools they carry, which built-in tools exist — the
engine cannot know any of it. Pass what you know:

```go
facts := lint.Facts{
    ServerNames:  func() []string { return live.Servers() },
    AllTools:     func() map[string][]string { return live.Tools() },
    ToolSchemas:  func() map[string][]byte { return live.Schemas() },
    BuiltinTools: func() []string { return registry.Names() },
    WriteServers: func() []string { return live.Writing() },
    SkillNames:   func() []string { return catalogue.Names() },
}
```

Every field is optional. A missing one **skips** the rules that need it and
records the reason in `Report.Skipped` *and* as an `X1` info finding. That is
deliberate: a linter that falls over because a dependency is down is a linter
people stop running, and a partial check that looks like a clean one is worse
than no check at all.

The same applies to the vocabulary in `Options` — and the library ships none of
it. The format deliberately leaves asset kinds, roles and calling conventions to
the application, and the WORDS a rule looks for belong to the application too:

```go
opts := lint.Options{
    Unmarshal: yaml.Unmarshal,
    // How your authors write "empty" in an instruction (W16).
    EmptyWords: []string{"empty", "пуст", "vide"},
    // Name fragments that mark a schema field as free text rather than a slot
    // (W13). Its structural half — a string inside an ARRAY — needs no words.
    FreeTextFields: []string{"message", "summary", "description", "raison"},
}
```

Every one of these stays off — loudly, as a recorded skip — until you say what
you call things. A rule that quietly reports nothing because nobody configured
it is indistinguishable from a clean skill, which is the one outcome this
package refuses to produce.

## The rules

| id | severity | catches | needs |
|---|---|---|---|
| S1 | error | the file parses, the header is legal, the format version is one the engine speaks | — |
| S3 | error | the playbook uses a construct you have removed | `Options.StaleAPIs` |
| S5 | warn | the playbook's size against the budget — it is context weight on every run | — |
| S6 | info | no `trigger_examples`: only reachable by being named outright | — |
| W1 | error | the description does not pass the engine's own validation | — |
| W2 | error | a server the program names is declared by the skill and registered | `Facts.ServerNames` |
| W3 | error | a `call` step's tool exists on its server | `Facts.AllTools` |
| W4 | warn | an asset is passed the way its kind implies — through the model's context or past it | `Options.Assets` |
| W5 | error | a `call` step carries the arguments its tool requires | `Facts.ToolSchemas` |
| W6 | error | somebody writes into the variable a loop collects | — |
| W7 | error | a built-in tool called by a step is declared in `builtin_tools` | — |
| W8 | error/warn | a wrapped call result is substituted whole where a field was meant | `Options.Envelopes` |
| W9 | error | an object in a response schema has at least one `required` field | — |
| W10 | error | `from:` in a call's arguments receives a handle, not the value's text | — |
| W11 | warn | an asset's `params` are keys the resolver actually reads | `Options.Assets` |
| W12 | warn | an instruction names a tool without saying how tools are called | `Options.CallProtocol`, `Facts.AllTools` |
| W13 | warn | a free-text field of a response schema has a length ceiling | `Options.FreeTextFields` (fields inside arrays need no vocabulary) |
| W14 | error | every reference names a variable that exists at that point in the flow | — |
| W15 | error | a declared built-in tool exists in the registry | `Facts.BuiltinTools` |
| W16 | error | a required field is not one the description beside it allows to be empty | `Options.EmptyWords` |
| W17 | error | `switch.var` is given a variable's name, not a `{{template}}` | — |
| W18 | warn | no alternative of a `contains` is already covered by a shorter one | — |
| E1 | error | every server the skill declares is registered | `Facts.ServerNames` |
| E2 | error | a tool the playbook calls exists on the server it names | `Options.CallProtocol`, `Facts.AllTools` |
| E3 | warn | a read-only skill does not reach for a server that writes | `Options.ReadOnlyRoles`, `Facts.WriteServers` |
| E4 | warn | a `delegate` step names a skill that exists | `Facts.SkillNames` |
| E5 | error | a built-in tool the playbook says to call is declared | `Facts.BuiltinTools` |
| X1 | info | a rule did not run, and why | — |

`lint.Rules()` returns the same catalogue as data. The numbers are owned by this
package: while rules were being added on both sides of the boundary they
collided, and a number meaning two things in two places is worse than no number.

## What it does not check, and why

- **Judgement by a model** — do two skills claim the same requests, is the
  description precise enough, is the instruction well written. That needs an
  intent matcher, a corpus of live phrasings and a judge model: the installation's
  business, not the format's.
- **Your own file wrapper** — front matter, a markdown body, fences with
  attributes. The format is one YAML document; anything you wrap around it, you
  check yourself.
- **Policy** — which role may do what, what a schedule requires, what blocks a
  save. The format deliberately does not close those lists.

## The limitation worth knowing before you rely on it

**The linter only sees contradictions the author spelled out.** W16 catches a
required field *because* the instruction beside it says "not named — an empty
string". Where the description says nothing about absence, there is nothing for
statics to grab, and the same failure goes through unseen. Finding those needs a
live run of the step against the real model with the real schema — not a rule.
