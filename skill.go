package skillengine

import (
	"fmt"
	"regexp"
	"strings"
)

// Skill — a whole skill file: the header plus the description of the turn.
//
// Why the header lives here and not in the embedding application. Every field
// below is already described by the format's schema (`skill.schema.yaml`), i.e.
// it belongs to the FORMAT — and yet each embedder used to declare its own
// struct for it, parse the file itself, and re-derive the same rules about
// which combinations are legal. Two copies of a contract drift by definition:
// the engine gains a field, the copy does not, and a skill that declares it
// loads with the field silently dropped.
//
// The engine reads exactly two of these fields (EngineVersion and the pair
// Workflow/Playbook chosen by Mode). The rest is here because the FILE has
// them and something must define their shape — the alternative is every
// embedder inventing it again.
type Skill struct {
	// EngineVersion — the format version this skill was written for, i.e. the
	// minimum engine it requires. Absent = 1.0.0 (see version.go).
	EngineVersion string `yaml:"skill_engine_version,omitempty"`
	// SkillVersion — the version of the skill itself. The engine does not read
	// it; measurements do — comparing "was 10 calls, now 3" without a version
	// compares something unknown with something unknown.
	SkillVersion string `yaml:"skill_version,omitempty"`

	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// TriggerExamples — live phrasings the skill must fire on. Routing is the
	// embedder's business; the field is part of the file, so it is part of the
	// type.
	TriggerExamples []string `yaml:"trigger_examples,omitempty"`

	// Servers — the MCP servers available to the skill: the CEILING of the
	// radius, which `workflow.tools` and a step's `tools` can only narrow.
	Servers []string `yaml:"servers,omitempty"`
	// BuiltinTools — the embedding application's own tools the skill needs
	// (as opposed to an MCP server's).
	BuiltinTools []string `yaml:"builtin_tools,omitempty"`

	// Role and Kind — names of the application's profiles: the format does not
	// close either list.
	Role string `yaml:"role,omitempty"`
	Kind string `yaml:"kind,omitempty"`

	// Temperature — a whole-skill override. Has no effect on the workflow path,
	// where generation parameters belong to the STEP (`sampling:`).
	Temperature *float64 `yaml:"temperature,omitempty"`
	// Disabled — turn the skill off without deleting the file.
	Disabled bool `yaml:"disabled,omitempty"`

	// Mode — which of the two descriptions runs (see ResolveMode). Empty = the
	// default, "there is a workflow → follow it".
	Mode string `yaml:"mode,omitempty"`
	// Workflow — the turn described in steps.
	Workflow *Flow `yaml:"workflow,omitempty"`
	// Playbook — the turn described as a free-form instruction. A full way to
	// describe a skill, not a draft: such a turn is run by the embedding
	// application, the engine takes no part in it.
	Playbook string `yaml:"playbook,omitempty"`

	// Meta — arbitrary data for the application: a schedule, an owner, tags.
	// The engine neither reads it nor checks its shape.
	Meta map[string]any `yaml:"meta,omitempty"`
}

// ParseSkill reads a skill file. Structure only: the result is not validated,
// so that a caller reporting on a file (a linter, an editor) can tell "this is
// not YAML" from "this is YAML that says something wrong" and point at each
// differently. For the second question call Validate.
//
// Unknown top-level keys are NOT rejected — a plain unmarshal function cannot
// be told to reject them, and demanding a particular YAML library would make
// the engine depend on one. That check belongs to the schema, which every
// embedder can run in an editor and in CI.
func ParseSkill(raw []byte, unmarshal Unmarshal) (Skill, error) {
	if unmarshal == nil {
		return Skill{}, fmt.Errorf("skill-engine: ParseSkill without an unmarshal function")
	}
	var s Skill
	if err := unmarshal(raw, &s); err != nil {
		return Skill{}, fmt.Errorf("skill-engine: parsing the skill: %w", err)
	}
	return s, nil
}

// Validate checks a parsed skill against the format: the version, the header's
// required fields, the mode against the descriptions present, and the workflow
// itself.
//
// The workflow is validated even when the mode points at the playbook. It sits
// in the file and one edit away from being run: a half you cannot switch to is
// exactly the failure `mode` exists to prevent — the author switches, the run
// falls over, and nothing said so while the file was being saved.
func (s *Skill) Validate() error {
	if err := CheckEngineVersion(s.EngineVersion); err != nil {
		return err
	}
	if err := ValidateSkillName(s.Name); err != nil {
		return err
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("skill-engine: skill %q has no description — routing decides by it", s.Name)
	}
	if _, err := s.ResolveMode(); err != nil {
		return err
	}
	if s.HasWorkflow() {
		return s.Workflow.Validate()
	}
	return nil
}

// ResolveMode decides which of the two descriptions this skill runs. See
// mode.go for the whole table.
func (s *Skill) ResolveMode() (Mode, error) {
	return ResolveMode(s.Mode, s.HasWorkflow(), s.HasPlaybook())
}

// HasWorkflow — the skill describes a turn in steps. A `workflow:` key with no
// steps under it does not count: it is an empty description, not a description
// of nothing to do.
func (s *Skill) HasWorkflow() bool { return s.Workflow != nil && len(s.Workflow.Steps) > 0 }

// HasPlaybook — the skill describes a turn in prose.
func (s *Skill) HasPlaybook() bool { return strings.TrimSpace(s.Playbook) != "" }

// skillNameRE — the name a skill file may carry. Kept identical to the pattern
// in the schema (a test compares them): the schema is what an editor and CI
// enforce, and two spellings of one rule mean a name that passes in one place
// and fails in the other.
var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,48}[a-z0-9]$|^[a-z0-9]$`)

// ValidateSkillName checks a skill's name against the format.
//
// The name is not decoration: it is the file's name, the key a delegate step
// refers to, and the label every measurement of the skill is grouped by. A name
// that differs from its file's by a capital letter looks the same in a report
// and is a different skill to the code.
func ValidateSkillName(name string) error {
	if name == "" {
		return fmt.Errorf("skill-engine: skill without a name")
	}
	if !skillNameRE.MatchString(name) {
		return fmt.Errorf("skill-engine: skill name %q: want lowercase letters, digits, `_` and `-`, "+
			"starting and ending with a letter or a digit, up to 50 characters", name)
	}
	return nil
}
