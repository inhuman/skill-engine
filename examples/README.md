# Examples

The format's schema (`skill.schema.yaml`) deliberately **does not close the
lists of values** for `role`, `kind`, `source`, `deliver`, `reasoning` or the
shape of `ref`: behind those words stands the design of a particular
application, and someone else's is of no use to yours.

Here are samples of what those slots get filled with, plus working skills from
different areas. These are EXAMPLES, not requirements: invent your own values.

Every file is checked by a test (`examples_test.go`): an example that stopped
parsing is worse than a missing one — it teaches the wrong thing.

| file | about | what it shows |
|---|---|---|
| [`vocabulary.yaml`](vocabulary.yaml) | a vocabulary of values | what the schema's open slots get filled with |
| [`pods.yaml`](pods.yaml) | listing machines in a cluster | parse the request → call → answer; the server is computed, but only within the declared set |
| [`weather.yaml`](weather.yaml) | weather through a browser | a `call` step without generation; the browser goes only to the step that needs it |
| [`proofread.yaml`](proofread.yaml) | proofreading text | a reference asset is SUBSTITUTED into the instruction — otherwise the model never reads it |
| [`expenses.yaml`](expenses.yaml) | spending as a chart | a code asset and the data go BY REFERENCE past the context; `deliver` is declared in advance |
| [`inbox.yaml`](inbox.yaml) | triaging an email | `switch` + `delegate`: in the "spam" branch the step that replies to the customer simply is not there |
| [`research.yaml`](research.yaml) | an answer from several sources | `parallel` and `<collect>.skipped` — "we never went" differs from "it was empty" |
| [`glossary.yaml`](glossary.yaml) | translating terms | `for_each` and `collect`; `in` takes a variable NAME, not a template |
| [`contract.yaml`](contract.yaml) | checking a contract | `if` + `exit` (wrong document — an honest exit) and an external asset with a `fetch` policy |
| [`triage.yaml`](triage.yaml) | triaging an incident | a composite skill: branching and delegation |
| [`audit.yaml`](audit.yaml) | checking a document | `profiles` shared by the classifier steps, and all four `on_empty` outcomes |
