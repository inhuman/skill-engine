package skillengine

// Named sets of step parameters.
//
// Why. In a live catalogue of 168 steps the instructions are nearly all
// unique, while the SHAPE around them repeats by the dozen: 32 steps carry the
// same model + temperature 0 + empty tool set + a structured answer, another 46
// carry a second such set. What repeats is not the step but its configuration —
// and a profile repeated 32 times is far past the point where a repeated set of
// parameters becomes a named constructor.
//
// A profile is not a step and cannot become one: it holds no instruction. That
// is deliberate — the thing worth sharing is the envelope, and the thing worth
// writing every time is the work.

import (
	"fmt"
	"strings"
)

// Profile — the generation parameters a step can inherit by name.
//
// Only the model step's envelope: what to generate with and how, plus the
// radius. No instruction, no save_as, no branching — those are the step's own
// work, and sharing them would be sharing the step itself (see the CHANGELOG on
// why named steps were not added).
type Profile struct {
	Model string `yaml:"model,omitempty"`
	// Sampling is replaced WHOLE, never merged key by key: a half-inherited
	// sampling block turns "why is my top_k from the profile and my temperature
	// my own" into a question asked at every debugging session.
	Sampling *Sampling `yaml:"sampling,omitempty"`
	// Tools — a pointer for the same reason as on a step: `tools: []` in a
	// profile must mean an EMPTY SET, not "unset". The empty set is the guard
	// ("do not go to that source"), and a profile that could not express it
	// would force the guard to be repeated on every step by hand — which is
	// what profiles exist to stop.
	Tools         *[]string   `yaml:"tools,omitempty"`
	MaxCalls      int         `yaml:"max_calls,omitempty"`
	MaxToolErrors int         `yaml:"max_tool_errors,omitempty"`
	OnError       ErrorPolicy `yaml:"on_error,omitempty"`

	// Misplaced — the same trap as on a step, and it is here for the same
	// reason: a profile also keeps its generation parameters in `sampling`, so
	// `max_tokens` written one level up is dropped by YAML without a word.
	// Refused by Validate; Run.Misplaced records what that silence cost.
	//
	// Worth having even though the measurement found no live case: a profile is
	// where the ceiling that DID work was written, so it is the shape an author
	// copies from — and copying it a level too high is precisely the mistake
	// this whole check exists for.
	Misplaced *Sampling `yaml:",inline" schema:"-"`
}

// applyProfiles folds each step's profile into the step itself.
//
// Done as a NORMALISATION pass, before validation: from there on nothing in the
// engine knows profiles exist — execution, tracing and every check see an
// ordinary step. That also makes the interaction with the response_schema rule
// fall out for free: a step whose model comes from a profile has a model by the
// time the rule is checked.
//
// Idempotent, because Validate runs on every ExecuteWith and skills are often
// validated by the embedder first: a field already set is never overwritten, so
// a second pass changes nothing.
func applyProfiles(steps []Step, profiles map[string]Profile, path string) error {
	for i := range steps {
		s := &steps[i]
		at := fmt.Sprintf("%s[%d]", path, i)
		if s.Name != "" {
			at = fmt.Sprintf("%s (%s)", at, s.Name)
		}
		if err := applyProfile(s, profiles, at); err != nil {
			return err
		}
		for _, br := range s.Branches() {
			if err := applyProfiles(br, profiles, at); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyProfile(s *Step, profiles map[string]Profile, at string) error {
	if s.Profile == "" {
		return nil
	}
	p, ok := profiles[s.Profile]
	if !ok {
		// Refused rather than ignored. The engine already has one class of
		// defect where an unknown name quietly resolves to nothing (variables),
		// and it is not worth repeating on steps: a step silently stripped of
		// its model and its tool radius still runs, and answers.
		return fmt.Errorf("%s: unknown profile %q", at, s.Profile)
	}
	if s.Run == nil || strings.TrimSpace(s.Run.Instruction) == "" {
		// A profile carries generation parameters; on a step that generates
		// nothing they have no effect at all. Accepting it silently is the
		// "declared, validated, and does nothing" class — the author would
		// believe the step runs on the profile's model.
		return fmt.Errorf("%s: profile %q on a step that is not an instruction step", at, s.Profile)
	}

	// The step wins over the profile, field by field: a profile is a default,
	// and a value written on the step is the author saying "not here".
	r := s.Run
	if r.Model == "" {
		r.Model = p.Model
	}
	if r.Sampling == nil {
		r.Sampling = p.Sampling
	}
	if r.Tools == nil {
		r.Tools = p.Tools
	}
	if r.MaxCalls == 0 {
		r.MaxCalls = p.MaxCalls
	}
	if r.MaxToolErrors == 0 {
		r.MaxToolErrors = p.MaxToolErrors
	}
	if r.OnError == "" {
		r.OnError = p.OnError
	}
	return nil
}

// validateProfiles checks the declarations themselves, before any step uses
// them.
func validateProfiles(profiles map[string]Profile) error {
	for name, p := range profiles {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("profile with an empty name")
		}
		if err := validateErrPolicy(p.OnError, "profile "+name); err != nil {
			return err
		}
		if p.MaxCalls < 0 {
			return fmt.Errorf("profile %q: max_calls is negative", name)
		}
		if p.MaxToolErrors < 0 {
			return fmt.Errorf("profile %q: max_tool_errors is negative", name)
		}
		if err := misplacedSamplingError("profile "+name, "profile", p.Misplaced); err != nil {
			return err
		}
	}
	return nil
}
