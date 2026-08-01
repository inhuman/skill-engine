package skillengine

import "fmt"

// Which of the two skill descriptions runs: steps (`workflow`) or a free-form
// instruction (`playbook`).
//
// The engine only executes steps — a `playbook` is run by the embedding
// application. The fork still lives here because it is FORMAT semantics, not a
// host detail: let every embedder implement it and the answer to "which of the
// two descriptions is in effect" starts to differ per host — while skills are
// portable and the format version promises otherwise.

// Mode — how a skill is executed.
type Mode string

const (
	ModeWorkflow Mode = "workflow"
	ModePlaybook Mode = "playbook"
)

// ResolveMode decides which description to run.
//
// declared is the skill's `mode` field; an empty string means it is unset.
// The two flags follow in the order "steps, text": whether a non-empty
// `workflow` and a non-empty `playbook` are present.
//
// The default (field unset) is steps when there are any: structure outranks
// prose, and that is also the direction of migration — move the prompt into
// steps and the turn follows them without touching configuration.
//
// An explicit mode with an empty half is an ERROR, not a fallback to the other
// one. A silent fallback looks like success: switch the mode to `playbook`,
// forget to write the text, and you get a clean run over the old steps and the
// conclusion "playbook works the same" — drawn from a turn the playbook never
// took part in.
func ResolveMode(declared string, hasWorkflow, hasPlaybook bool) (Mode, error) {
	switch Mode(declared) {
	case "":
		switch {
		case hasWorkflow:
			return ModeWorkflow, nil
		case hasPlaybook:
			return ModePlaybook, nil
		default:
			return "", fmt.Errorf("skill describes no turn: neither workflow nor playbook")
		}

	case ModeWorkflow:
		if !hasWorkflow {
			return "", fmt.Errorf("mode: workflow, but there are no steps")
		}
		return ModeWorkflow, nil

	case ModePlaybook:
		if !hasPlaybook {
			return "", fmt.Errorf("mode: playbook, but there is no text")
		}
		return ModePlaybook, nil

	default:
		return "", fmt.Errorf("mode %q: want workflow or playbook", declared)
	}
}
