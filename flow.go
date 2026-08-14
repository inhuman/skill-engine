// Package skillengine executes a declarative skill description: a sequence of
// steps, each with its own tool set, its own call budget and its own policy on
// failure.
//
// Why. A restriction written in words is followed probabilistically by a model:
// a measured example — the rule "do not go to this source" produced 53 attempts
// and not one successful read in a month. The absence of that source from a
// step's tool set is something it cannot fail to follow.
//
// The shape is borrowed from AgentSPEX (arXiv 2604.13346, Apache 2.0), a YAML
// language for specifying agent workflows. The FORM is taken, not the code:
// that one is Python with its own harness and its own sandbox. The subset was
// chosen empirically: three live skills written out in this syntax used steps,
// branching and variables — and never parallel/while/gather.
//
// The package is written to be DETACHABLE: the domain (tools, RBAC, telemetry)
// lives behind the interfaces below, and nothing specific to a particular agent
// is imported inside.
package skillengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Flow — a parsed description: the set of steps executed in order.
type Flow struct {
	// Steps run sequentially; branching happens inside a step (Switch/If).
	Steps []Step `yaml:"steps"`
	// Tools — the tool set available to the whole flow. A step can only narrow
	// it, never widen it: widening would make the restriction meaningless.
	Tools []string `yaml:"tools,omitempty"`
	// Vars — initial values of the flow's variables.
	Vars map[string]string `yaml:"vars,omitempty"`
	// Assets — named payloads passed to tools by reference (see asset.go).
	// Declared in the skill rather than in a step: the same payload is often
	// consumed by several steps.
	Assets map[string]Asset `yaml:"assets,omitempty"`
	// Profiles — named sets of step parameters (see profile.go). Folded into
	// the steps that name them before validation, so nothing downstream knows
	// they existed.
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
}

// Step — a single step. Exactly one of Run/Switch/If/Set must be filled in;
// Validate checks that.
type Step struct {
	Name string `yaml:"name,omitempty"`

	// Run — a step performed by the model: an instruction plus a result stored
	// in a variable.
	Run *Run `yaml:",inline"`
	// Switch — branching on a variable's value.
	Switch *Switch `yaml:"switch,omitempty"`
	// If — branching on a condition.
	If *If `yaml:"if,omitempty"`
	// Set — computing a variable without involving the model.
	Set *Set `yaml:"set,omitempty"`
	// Call — calling a tool without involving the model.
	Call *Call `yaml:"call,omitempty"`
	// Exit — stop the flow and return the turn to its previous path.
	Exit *Exit `yaml:"exit,omitempty"`
	// Delegate — hand the work to another skill (see the Delegate type).
	Delegate *Delegate `yaml:"delegate,omitempty"`
	// Parallel — several independent branches at once (see the Parallel type).
	Parallel *Parallel `yaml:"parallel,omitempty"`
	// ForEach — repeat steps for every item of a collection.
	ForEach *ForEach `yaml:"for_each,omitempty"`

	// OnServer — which server the step runs on. Supports {{var}}: the name is
	// computed at execution time.
	//
	// For call: the address of the call; for a model step: whose tools it is
	// handed. Why: 14 of 28 live skills carry VARIANTS of one server (gitlab
	// dev/prod, five k8s clusters, prometheus dev/prod), and without a runtime
	// choice each needs a switch of 2–5 nearly identical branches — the very
	// duplication that diverges on the first edit.
	//
	// Permissions are not weakened: the SUBSTITUTED name is checked, and it must
	// belong to the flow's set.
	OnServer string `yaml:"on_server,omitempty"`

	// Profile — the name of a set of generation parameters to inherit (see
	// profile.go). Whatever the step spells out itself wins; the rest comes
	// from the profile.
	Profile string `yaml:"profile,omitempty"`

	// When — the step's applicability condition (same syntax as If.Cond). False
	// → the step is skipped and the flow moves on.
	//
	// Why separate from If: it is expressible with branching too, but `when`
	// reads as a PROPERTY of the step and does not grow nesting where there is
	// no branch — only "do it or not". Live class: the task matches the skill
	// only partially, and then the turn "sometimes gives the right answer,
	// sometimes not" with no visible reason. A conditional skip is visible in
	// the events.
	When string `yaml:"when,omitempty"`
}

// Exit — leaving the skill: the flow stops and the turn returns to its ordinary
// path.
//
// Needed because steps execute in full whatever they are given: a step has no
// reason to doubt that the skill was chosen correctly. Exit makes the doubt
// expressible.
//
// The solution is not a new mechanism but a new STEP KIND: the program already
// has a classifier, and all it needs is a "not my case" value and a branch with
// exit.
type Exit struct {
	// Reason — why we left. Goes into the event and the log: these strings are
	// what routing misses are diagnosed from.
	Reason string `yaml:"reason,omitempty"`
}

// Call — a direct tool call, WITHOUT the model.
//
// Why. A step executed by the model costs two generations even when there is
// nothing to decide: one to decide to call the tool, another to retell its
// result. Measured: 6 generations for 2 tool calls. Meanwhile the call itself is
// often DETERMINISTIC — all the arguments already sit in the flow's variables
// (computed by earlier steps or by Set).
//
// The difference from Run: there is no instruction, no choice and no retelling
// — the tool's result lands in the variable verbatim. A whole class of "the
// model called the tool in the wrong envelope" errors disappears with it.
//
// Permissions are NOT bypassed: the call goes the same way as a call from the
// model and passes the same checks. A step cannot reach a tool the caller is
// not allowed.
type Call struct {
	// Tool — which tool to call, in the form "server:tool".
	Tool string `yaml:"tool"`
	// Args — the call's arguments. Values support {{var}} substitution; it
	// applies to strings ONLY, the structure stays as written.
	Args map[string]any `yaml:"args,omitempty"`
	// SaveAs — the variable for the result ("" = the result is not stored).
	SaveAs string `yaml:"save_as,omitempty"`
	// OnError — what to do on failure. Default is Abort, as for Run.
	OnError ErrorPolicy `yaml:"on_error,omitempty"`
	// OnEmpty — what an empty tool result means (see empty.go). A tool that
	// returned nothing is the same class as a model that said nothing, so the
	// field exists on both paths; `retry` is the exception, refused here
	// because a call cannot be repeated.
	OnEmpty EmptyPolicy `yaml:"on_empty,omitempty"`
	// OnEmptyValue — what to store instead, for OnEmpty: use. Supports {{var}}.
	OnEmptyValue string `yaml:"on_empty_value,omitempty"`
}

// Run — a step executed by the model.
type Run struct {
	// Instruction — what to do. Supports {{var}} substitution.
	Instruction string `yaml:"instruction,omitempty"`
	// SaveAs — the variable name for the result ("" = the result is not stored).
	SaveAs string `yaml:"save_as,omitempty"`

	// Tools — the tools of THIS step. An empty one inside a non-empty flow = the
	// step is handed no tools at all (it must answer from what is already
	// gathered). A pointer is what tells "unset" from "empty list".
	Tools *[]string `yaml:"tools,omitempty"`

	// MaxCalls — the ceiling on tool calls in this step. A step's field, not the
	// flow's: in live skills the limits differ — one attempt for a code search,
	// up to eight for walking a tree.
	//
	// 0 is NOT "unlimited": the executor substitutes its own ceiling (8
	// generations for ours). An unlimited step is deliberately absent from the
	// format — a step without a ceiling brings back exactly the problem the
	// format was started for.
	MaxCalls int `yaml:"max_calls,omitempty"`

	// MaxToolErrors — how many of the model's MISSES are forgiven beyond
	// max_calls.
	//
	// A miss is a call rejected by the tool (a required argument forgotten,
	// broken JSON). It costs a generation, so it cannot be free, but neither
	// should it eat the budget of USEFUL work: live failure — a review step
	// burned all 6 calls on misses (6 of 7 rejected by the server) and read not
	// a single diff.
	//
	// 0 is the executor's default (2), not "unlimited": endless attempts bring
	// back exactly the problem the format was started for. Exceeding it stops
	// the step with degraded and a named reason, not silently.
	MaxToolErrors int `yaml:"max_tool_errors,omitempty"`

	// OnError — what to do when the step could not. Default is Abort.
	OnError ErrorPolicy `yaml:"on_error,omitempty"`

	// OnEmpty — what an empty result means (see empty.go). Default is
	// EmptyContinue, which is what the engine did before the field existed.
	OnEmpty EmptyPolicy `yaml:"on_empty,omitempty"`
	// OnEmptyValue — what to store instead, for OnEmpty: use. Supports {{var}}.
	OnEmptyValue string `yaml:"on_empty_value,omitempty"`
	// OnEmptyRetries — how many times OnEmpty: retry runs the step again.
	// Unset = DefaultEmptyRetries.
	OnEmptyRetries int `yaml:"on_empty_retries,omitempty"`

	// OneOf — the allowed values of the step's result. Set → the result is
	// normalised: whichever of the listed values is found in the answer is
	// taken; if none is found, the variable goes empty.
	//
	// Why. A classifier step exists for the sake of branching, while a model
	// answers in prose: asked to "answer in one word: t1 or foreign" it replies
	// "Summary: determined the type by prefix. Result: foreign" — correct in
	// meaning and useless to a switch that compares exactly. Live failure: no
	// branch was chosen and the turn fell through to default.
	//
	// This is NOT a format check asked of the model (a request it follows
	// probabilistically) but normalisation ON THE WAY OUT, in code.
	OneOf []string `yaml:"one_of,omitempty"`

	// Model — the model of THIS step (empty = the executor's default).
	Model string `yaml:"model,omitempty"`
	// Sampling — the step's generation parameters (see the Sampling type).
	Sampling *Sampling `yaml:"sampling,omitempty"`
	// ResponseSchema — the schema of the step's structured answer. The result is
	// stored in the variable as an object, fields reachable as {{var.field}}.
	//
	// Meaningful ONLY where decoding grammar works: on the way to the model the
	// schema is dropped silently, and a structured answer would degenerate into
	// "the model usually answers JSON" — an undetectable hole. So the executor
	// MUST refuse if the path to the step's model does not carry the grammar,
	// rather than quietly continue.
	//
	// For the same reason Model is REQUIRED alongside, and Validate enforces
	// it: leaving the choice to a default means whichever model the executor
	// happens to use decides whether the schema applies at all.
	ResponseSchema map[string]any `yaml:"response_schema,omitempty"`
}

// Delegate — hand the work to another skill.
//
// This is what composite skills do: incident triage calls ticket creation, a
// search skill calls a search skill and a reference skill. Without such a step a
// composite handed a description would execute as an ordinary flow and lose
// delegation entirely — that is, stop doing its job.
//
// The radius is the radius of the CALLED skill: it declares its own servers, and
// delegation does not widen the caller's access.
type Delegate struct {
	Skill   string      `yaml:"skill"`
	Task    string      `yaml:"task"`
	SaveAs  string      `yaml:"save_as,omitempty"`
	OnError ErrorPolicy `yaml:"on_error,omitempty"`
}

// Parallel — independent branches at once: in live skills this is gathering
// evidence from different sources in parallel.
//
// Branches DO NOT SEE each other's variables: otherwise the result would depend
// on the order of completion, which is non-deterministic — and the format exists
// for the opposite.
type Parallel struct {
	Branches [][]Step    `yaml:"branches"`
	Collect  string      `yaml:"collect,omitempty"`
	OnError  ErrorPolicy `yaml:"on_error,omitempty"`
}

// ForEach — repeat steps for every item of a collection.
//
// Live cases that cannot be expressed otherwise: "for EACH of the 5 services
// find the repository and the commit", "for every service of the release from
// versions.yaml", "for every service across all the tables". The list's length
// is not known in advance — it cannot be unrolled into fixed steps.
type ForEach struct {
	// In — the variable holding the collection. An array (say, from a structured
	// answer) is iterated element-wise; a string, by its non-empty lines. The
	// latter covers the observed case of "a list produced by a tool".
	In string `yaml:"in"`
	// As — the name of the item variable inside the loop body.
	As    string `yaml:"as"`
	Steps []Step `yaml:"steps"`
	// Collect — the variable the iterations' results are gathered into. It is
	// emptied before each iteration, so what is gathered is what THAT iteration
	// produced: one whose branch did not fire contributes nothing instead of
	// repeating whatever the previous one left behind.
	Collect string `yaml:"collect,omitempty"`
	// MaxIterations — the ceiling. Required IN SPIRIT even when not set
	// explicitly: a loop over a collection of unknown length is a straight road
	// to a runaway, and the session budget will stop it only after the turn has
	// been burned.
	MaxIterations int         `yaml:"max_iterations,omitempty"`
	OnError       ErrorPolicy `yaml:"on_error,omitempty"`
}

// DefaultMaxIterations — the loop ceiling when the skill did not set its own.
const DefaultMaxIterations = 10

// SkillDelegate executes a delegate step. The third (and last) point where the
// package touches the outside world — next to Runner and ToolCaller.
type SkillDelegate interface {
	Delegate(ctx context.Context, skill, task string) (string, error)
}

// Sampling — a step's generation parameters.
//
// The unit of tuning is the STEP, not the skill, for the same reason tools and
// max_calls belong to a step: steps do different work. A classifier needs zero
// temperature — it picks between two values; wording an answer for a human needs
// it warmer, or the text comes out wooden. One parameter for the whole skill
// serves both badly.
type Sampling struct {
	Temperature       *float32 `yaml:"temperature,omitempty"`
	TopP              *float32 `yaml:"top_p,omitempty"`
	TopK              *int     `yaml:"top_k,omitempty"`
	MinP              *float64 `yaml:"min_p,omitempty"`
	RepetitionPenalty *float64 `yaml:"repetition_penalty,omitempty"`
	// MaxTokens — the output-token ceiling of THIS step. Unset — the global
	// MaxOutputTokens applies.
	//
	// Needed where a step is short by nature while a runaway generation is
	// expensive: two chunks of a review produced 32768 and 22482 tokens instead
	// of the usual ~2300, hitting the global ceiling, and one of them broke its
	// own structured answer in the process. A global knob is no help here — it
	// serves consumers with different envelopes.
	MaxTokens *int `yaml:"max_tokens,omitempty"`
	// Reasoning — reasoning depth (low|medium|high) on models that support it.
	// Set on the STEP that needs it: a project measurement showed that high on
	// the investigator role ate 83% of subagent time.
	Reasoning string `yaml:"reasoning,omitempty"`
}

// Switch — branching on a variable's value.
type Switch struct {
	Var     string            `yaml:"var"`
	Cases   map[string][]Step `yaml:"cases"`
	Default []Step            `yaml:"default,omitempty"`
}

// If — branching on a condition: "var == value", "var is [not] empty",
// "var contains a | b", "var > 5" (see cond.go for the whole grammar).
type If struct {
	Cond string `yaml:"cond"`
	Then []Step `yaml:"then"`
	Else []Step `yaml:"else,omitempty"`
}

// Set — assigning a variable. The value supports {{var}} substitution.
type Set struct {
	Var   string `yaml:"var"`
	Value string `yaml:"value"`
}

// ErrorPolicy — the reaction to a step's failure.
//
// Three classes of failure are told apart deliberately: a permission refusal
// (the tool exists but the caller is not allowed it) calls for different
// behaviour than an empty result. The policy was repeated verbatim by three
// different skill authors — a sure sign it belongs to the engine rather than to
// the skill.
type ErrorPolicy string

const (
	// PolicyAbort — stop the flow (default).
	PolicyAbort ErrorPolicy = "abort"
	// PolicyContinue — record the failure into the step's variable and move on.
	// This is how "no permission — say so and continue with what is available"
	// is expressed.
	PolicyContinue ErrorPolicy = "continue"
	// PolicySkip — skip the rest of the current branch without aborting the flow.
	PolicySkip ErrorPolicy = "skip"
)

// Result — a step's outcome, as it lands in the flow's variables.
type Result struct {
	Text  string
	Calls int
	// CallsFailed — how many of them FAILED. A step with calls=7 and zero useful
	// results is outwardly indistinguishable from one that worked, and the
	// digging has to happen in pod logs.
	CallsFailed int
	// Note — the reason the EXECUTOR knows and the engine cannot derive:
	// "stopped after too many misses" versus "hit the ceiling". Empty — the
	// engine judges for itself. Without it the precise reason drowns in a generic
	// "the step produced no text", and fixing happens blind (we stepped on this
	// ourselves).
	Note string
	// Truncated — the answer is unfinished: upstream cut the generation off at
	// the token limit. Separate from Note because it is a MACHINE signal: it is
	// what a decision to replay the step is based on, while Note is the human
	// reason for degradation in a report.
	Truncated bool
	Err       error
}

// validateCall checks a call's shape before execution: the tool name must carry
// both the server and the tool — otherwise the call has no known address.
func validateCall(c *Call, at string, serverFromStep bool) error {
	if serverFromStep {
		// The server is set by the step (on_server) — the tool name alone is
		// enough in tool.
		if strings.TrimSpace(c.Tool) == "" {
			return fmt.Errorf("%s: call without a tool name", at)
		}
		return validateErrPolicy(c.OnError, at)
	}
	server, tool, ok := SplitToolRef(c.Tool)
	if !ok {
		return fmt.Errorf("%s: call %q: want \"server:tool\" (or the server in on_server)", at, c.Tool)
	}
	if server == "" || tool == "" {
		return fmt.Errorf("%s: call %q: empty server or tool", at, c.Tool)
	}
	return validateErrPolicy(c.OnError, at)
}

// validateCallEmpty checks a call's emptiness policy. Separate from
// validateCall because that one returns early on the server-from-step path,
// and this rule applies to both.
func validateCallEmpty(c *Call, at string) error {
	return validateEmptyPolicy(c.OnEmpty, c.OnEmptyValue, 0, true, at)
}

func validateErrPolicy(p ErrorPolicy, at string) error {
	switch p {
	case "", PolicyAbort, PolicyContinue, PolicySkip:
		return nil
	default:
		return fmt.Errorf("%s: unknown on_error %q", at, p)
	}
}

// SplitToolRef parses "server:tool". The separator is the FIRST colon: tool
// names contain colons, server names do not.
func SplitToolRef(ref string) (server, tool string, ok bool) {
	i := strings.Index(ref, ":")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(ref[:i]), strings.TrimSpace(ref[i+1:]), true
}

// ToolCaller calls a tool directly. The second (and last) point where the
// package touches the outside world — next to Runner.
type ToolCaller interface {
	CallTool(ctx context.Context, server, tool string, args map[string]any) (string, error)
}

// MemoryReader returns a tool's full result by its working-memory handle.
//
// An interface rather than the host's type: the package must stay detachable,
// and all it knows about the application's working memory is exactly this — "a
// string handle can be turned into a string".
type MemoryReader interface {
	Get(id string) (string, bool)
}

// Runner executes a single step: gives the model an instruction, allowing
// exactly the listed tools, and returns the answer's text.
//
// This is the ONLY point of contact with the outside world: everything the
// package knows about models, tools, permissions and telemetry hides behind this
// interface.
type Runner interface {
	Run(ctx context.Context, req StepRequest) (Result, error)
}

// StepRequest — what a step's executor needs.
type StepRequest struct {
	Name        string
	Instruction string
	// Tools — the exact set of allowed tools. nil = the flow's set.
	Tools    []string
	MaxCalls int
	// MaxToolErrors — how many of the model's misses are forgiven beyond
	// MaxCalls.
	MaxToolErrors int
	// Model / Sampling — what to generate the step with and how. Empty = the
	// executor's defaults.
	Model          string
	Sampling       *Sampling
	ResponseSchema map[string]any
	// OneOf — the allowed values of the result. An executor whose model holds a
	// decoding grammar passes them as an enum: then a value outside the list
	// becomes IMPOSSIBLE rather than corrected after the fact.
	OneOf []string
}

// ErrDenied — a permission refusal. Returned by a Runner so the policy can tell
// "not allowed" from "broke": retries and workarounds are pointless for the
// former and sometimes sensible for the latter.
var ErrDenied = errors.New("skill-engine: denied")

// ErrExit — the flow was stopped by an `exit` step. NOT an execution error: the
// caller must tell it from a failure, because the reaction is the opposite — not
// "show what we gathered" but "this skill does not fit, take the ordinary path".
var ErrExit = errors.New("skill-engine: exit")

// ExitError carries the reason for leaving up to the caller.
type ExitError struct{ Reason string }

func (e *ExitError) Error() string {
	if e.Reason == "" {
		return ErrExit.Error()
	}
	return ErrExit.Error() + ": " + e.Reason
}

func (e *ExitError) Is(target error) bool { return target == ErrExit }

// ToolRef — a tool declared by a skill.
type ToolRef struct {
	Server string
	Tool   string
	// Dynamic — the server is computed at execution time ({{var}} in on_server),
	// i.e. it is statically unknown: permissions must be checked against the
	// flow's whole set.
	Dynamic bool
}

// DeclaredTools lists the tools the flow may call.
//
// A pure function over the description: permissions are checked by the caller,
// the engine knows nothing of them. The point is to learn about a refusal BEFORE
// the first generation rather than halfway through, with steps already burned.
func (f *Flow) DeclaredTools() []ToolRef {
	var out []ToolRef
	collectTools(f.Steps, f.Tools, &out)
	return out
}

func collectTools(steps []Step, flowTools []string, out *[]ToolRef) {
	for _, s := range steps {
		dynamic := strings.Contains(s.OnServer, "{{")
		switch {
		case s.Call != nil:
			server, tool, _ := SplitToolRef(s.Call.Tool)
			if s.OnServer != "" {
				if !dynamic {
					server = s.OnServer
				}
				if _, bare, ok := SplitToolRef(s.Call.Tool); ok {
					tool = bare
				} else {
					tool = s.Call.Tool
				}
			}
			*out = append(*out, ToolRef{Server: server, Tool: tool, Dynamic: dynamic})
		case s.Run != nil && strings.TrimSpace(s.Run.Instruction) != "":
			// For a model step the specific tool is chosen by the model — only
			// the SERVER is statically known.
			servers := flowTools
			if s.Run.Tools != nil {
				servers = *s.Run.Tools
			}
			if s.OnServer != "" && !dynamic {
				servers = []string{s.OnServer}
			}
			for _, srv := range servers {
				*out = append(*out, ToolRef{Server: srv, Dynamic: dynamic})
			}
		}
		if s.Switch != nil {
			for _, br := range s.Switch.Cases {
				collectTools(br, flowTools, out)
			}
			collectTools(s.Switch.Default, flowTools, out)
		}
		if s.If != nil {
			collectTools(s.If.Then, flowTools, out)
			collectTools(s.If.Else, flowTools, out)
		}
		if s.ForEach != nil {
			collectTools(s.ForEach.Steps, flowTools, out)
		}
		if s.Parallel != nil {
			for _, br := range s.Parallel.Branches {
				collectTools(br, flowTools, out)
			}
		}
	}
}

// Validate checks a description before execution: an empty flow, a step with no
// action, two actions in one step, a reference to an unknown policy.
//
// It does NOT touch the description it was given. Validation needs the steps in
// the shape execution reads them — profiles folded in, a `save_as` written
// beside a call moved into it — and doing that in place had two consequences
// worth avoiding: a caller holding a parsed Flow saw the engine's working copy
// instead of the file they parsed, and two turns over the same *Flow wrote to
// the same structs from two goroutines. "Parse once, run many times" is the
// natural way to use a skill engine, so it has to be the safe one.
func (f *Flow) Validate() error {
	_, err := f.normalized()
	return err
}

// normalized returns the description as EXECUTION reads it: a copy of the flow
// with the steps normalised, profiles folded in, and everything checked.
//
// The copy is the point. Only the steps are copied — assets, vars and profiles
// are read and never written, so they are shared.
func (f *Flow) normalized() (*Flow, error) {
	if f == nil || len(f.Steps) == 0 {
		return nil, fmt.Errorf("skill-engine: empty flow — not a single step")
	}
	out := *f
	out.Steps = cloneSteps(f.Steps)

	if err := normalizeSteps(out.Steps, "steps"); err != nil {
		return nil, err
	}
	if err := validateAssets(out.Assets); err != nil {
		return nil, fmt.Errorf("skill-engine: %w", err)
	}
	if err := validateProfiles(out.Profiles); err != nil {
		return nil, fmt.Errorf("skill-engine: %w", err)
	}
	// Before validateSteps, deliberately: from here on a step that inherited
	// its model from a profile HAS a model, so every check downstream — the
	// response_schema pairing above all — sees the step as it will execute.
	if err := applyProfiles(out.Steps, out.Profiles, "steps"); err != nil {
		return nil, err
	}
	if err := validateSteps(out.Steps, "steps"); err != nil {
		return nil, err
	}
	return &out, nil
}

// cloneSteps deep-copies the steps down to everything normalisation writes to:
// the step itself, the Run/Call/Delegate it points at, and the nested step
// lists of every branching kind.
//
// Anything not copied here is something the engine only READS. If a future
// pass starts writing to a field that is still shared, this is where it has to
// be added — and the tests around Validate are what will notice.
func cloneSteps(steps []Step) []Step {
	if steps == nil {
		return nil
	}
	out := make([]Step, len(steps))
	for i, s := range steps {
		out[i] = s // the value, then everything it points at
		if s.Run != nil {
			run := *s.Run
			out[i].Run = &run
		}
		if s.Call != nil {
			call := *s.Call
			out[i].Call = &call
		}
		if s.Delegate != nil {
			d := *s.Delegate
			out[i].Delegate = &d
		}
		if s.Switch != nil {
			sw := *s.Switch
			sw.Cases = make(map[string][]Step, len(s.Switch.Cases))
			for k, br := range s.Switch.Cases {
				sw.Cases[k] = cloneSteps(br)
			}
			sw.Default = cloneSteps(s.Switch.Default)
			out[i].Switch = &sw
		}
		if s.If != nil {
			ifs := *s.If
			ifs.Then = cloneSteps(s.If.Then)
			ifs.Else = cloneSteps(s.If.Else)
			out[i].If = &ifs
		}
		if s.ForEach != nil {
			fe := *s.ForEach
			fe.Steps = cloneSteps(s.ForEach.Steps)
			out[i].ForEach = &fe
		}
		if s.Parallel != nil {
			par := *s.Parallel
			par.Branches = make([][]Step, len(s.Parallel.Branches))
			for j, br := range s.Parallel.Branches {
				par.Branches[j] = cloneSteps(br)
			}
			out[i].Parallel = &par
		}
	}
	return out
}

// normalizeSteps brings a description to the shape execution reads it in.
//
// A model step declares save_as at its own level (Run is inlined into the step),
// while call declares it inside itself. The difference is invisible to the eye,
// and an author writes save_as where they are used to. The result was silently
// not stored: the next step got an empty string and decided blind — live case: a
// judging search skill passed its verdict on an empty list. So a save_as written
// at step level next to a call is accepted as belonging to the call.
func normalizeSteps(steps []Step, path string) error {
	for i := range steps {
		s := &steps[i]
		at := fmt.Sprintf("%s[%d]", path, i)
		if s.Name != "" {
			at = fmt.Sprintf("%s (%s)", at, s.Name)
		}
		// A call step's on_empty written at step level, where on_error and
		// save_as also live for a model step. Left in place it would sit in Run,
		// which a call step never reads — declared, valid, and doing nothing.
		if s.Call != nil && s.Run != nil && strings.TrimSpace(s.Run.Instruction) == "" {
			switch {
			case s.Run.OnEmpty != "" && s.Call.OnEmpty != "" && s.Run.OnEmpty != s.Call.OnEmpty:
				return fmt.Errorf("%s: on_empty given twice and differently (%q on the step, %q inside call)",
					at, s.Run.OnEmpty, s.Call.OnEmpty)
			case s.Run.OnEmpty != "" && s.Call.OnEmpty == "":
				s.Call.OnEmpty, s.Run.OnEmpty = s.Run.OnEmpty, ""
			}
			if s.Run.OnEmptyValue != "" && s.Call.OnEmptyValue == "" {
				s.Call.OnEmptyValue, s.Run.OnEmptyValue = s.Run.OnEmptyValue, ""
			}
		}
		if s.Run != nil && strings.TrimSpace(s.Run.Instruction) == "" && s.Run.SaveAs != "" {
			switch {
			case s.Call != nil && s.Call.SaveAs != "" && s.Call.SaveAs != s.Run.SaveAs:
				return fmt.Errorf("%s: save_as given twice and differently (%q on the step, %q inside call)",
					at, s.Run.SaveAs, s.Call.SaveAs)
			case s.Call != nil:
				s.Call.SaveAs = s.Run.SaveAs
				s.Run = nil
			case s.Delegate != nil && s.Delegate.SaveAs == "":
				s.Delegate.SaveAs = s.Run.SaveAs
				s.Run = nil
			}
		}
		for _, br := range s.Branches() {
			if err := normalizeSteps(br, at); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSteps(steps []Step, path string) error {
	for i, s := range steps {
		at := fmt.Sprintf("%s[%d]", path, i)
		if s.Name != "" {
			at = fmt.Sprintf("%s (%s)", at, s.Name)
		}
		n := 0
		// `on_error` is a STEP-level key and lands in the inline Run whatever the
		// step does — so it is checked here rather than beside the instruction.
		// Since a reference became a path, `set`, `switch` and `if` can fail too
		// and their policy is read from this same field; an unknown value there
		// would quietly mean abort, which is a declaration without an effect.
		if s.Run != nil {
			switch s.Run.OnError {
			case "", PolicyAbort, PolicyContinue, PolicySkip:
			default:
				return fmt.Errorf("%s: unknown on_error %q", at, s.Run.OnError)
			}
		}
		if s.Run != nil && strings.TrimSpace(s.Run.Instruction) != "" {
			n++
			if s.Run.MaxCalls < 0 {
				return fmt.Errorf("%s: max_calls is negative", at)
			}
			if err := validateEmptyPolicy(s.Run.OnEmpty, s.Run.OnEmptyValue, s.Run.OnEmptyRetries, false, at); err != nil {
				return err
			}
		}
		// A response_schema without a model is a silent hole: the decoding
		// grammar does not survive every path to a model, and where it is
		// dropped a "structured answer" degenerates into "the model usually
		// answers JSON" — parsed by luck, and failing without a trace when the
		// luck runs out.
		//
		// The schema has demanded the pair from the start, but the schema is
		// only enforced by an embedder who runs a JSON-schema validator; the
		// engine cannot run one (it would be a dependency). So the rule is
		// duplicated here on purpose — the one place every embedder passes
		// through. Checked outside the instruction branch because the schema
		// attaches it to the STEP: a stray response_schema on a `call` step is
		// the same hole, minus the model that could have honoured it.
		if s.Run != nil && len(s.Run.ResponseSchema) > 0 && strings.TrimSpace(s.Run.Model) == "" {
			return fmt.Errorf("%s: response_schema without model — the decoding grammar is not guaranteed on the default path", at)
		}
		if s.Switch != nil {
			n++
			if strings.TrimSpace(s.Switch.Var) == "" {
				return fmt.Errorf("%s: switch without var", at)
			}
			for k, br := range s.Switch.Cases {
				if err := validateSteps(br, at+".cases."+k); err != nil {
					return err
				}
			}
			if err := validateSteps(s.Switch.Default, at+".default"); err != nil {
				return err
			}
		}
		if s.If != nil {
			n++
			if _, _, _, err := parseCond(s.If.Cond); err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			if err := validateSteps(s.If.Then, at+".then"); err != nil {
				return err
			}
			if err := validateSteps(s.If.Else, at+".else"); err != nil {
				return err
			}
		}
		if s.Set != nil {
			n++
			if strings.TrimSpace(s.Set.Var) == "" {
				return fmt.Errorf("%s: set without var", at)
			}
		}
		if s.Call != nil {
			n++
			if err := validateCall(s.Call, at, s.OnServer != ""); err != nil {
				return err
			}
			if err := validateCallEmpty(s.Call, at); err != nil {
				return err
			}
		}
		if s.Exit != nil {
			n++
		}
		if s.Delegate != nil {
			n++
			if strings.TrimSpace(s.Delegate.Skill) == "" {
				return fmt.Errorf("%s: delegate without skill", at)
			}
			if strings.TrimSpace(s.Delegate.Task) == "" {
				return fmt.Errorf("%s: delegate without task", at)
			}
			if err := validateErrPolicy(s.Delegate.OnError, at); err != nil {
				return err
			}
		}
		if s.ForEach != nil {
			n++
			if strings.TrimSpace(s.ForEach.In) == "" {
				return fmt.Errorf("%s: for_each without in", at)
			}
			// `in` is a variable NAME, not a template. The field looks as though
			// it would accept "{{parts}}", and silently will not: the engine
			// looks for a variable with that name, does not find it, the loop
			// makes ZERO iterations and reports "ok". Refusing at parse time is
			// cheaper: an empty walk looks like a successful one.
			if strings.Contains(s.ForEach.In, "{{") {
				return fmt.Errorf("%s: for_each.in = %q — this is a variable NAME, without {{ }}",
					at, s.ForEach.In)
			}
			if strings.TrimSpace(s.ForEach.As) == "" {
				return fmt.Errorf("%s: for_each without as", at)
			}
			if s.ForEach.MaxIterations < 0 {
				return fmt.Errorf("%s: max_iterations is negative", at)
			}
			if err := validateSteps(s.ForEach.Steps, at+".for_each"); err != nil {
				return err
			}
			if err := validateErrPolicy(s.ForEach.OnError, at); err != nil {
				return err
			}
		}
		if s.Parallel != nil {
			n++
			if len(s.Parallel.Branches) < 2 {
				return fmt.Errorf("%s: parallel with fewer than two branches — that is an ordinary sequence", at)
			}
			for i, br := range s.Parallel.Branches {
				if err := validateSteps(br, fmt.Sprintf("%s.branches[%d]", at, i)); err != nil {
					return err
				}
			}
			if err := validateErrPolicy(s.Parallel.OnError, at); err != nil {
				return err
			}
		}
		if s.When != "" {
			if _, _, _, err := parseCond(s.When); err != nil {
				return fmt.Errorf("%s: when: %w", at, err)
			}
		}
		switch n {
		case 1:
		case 0:
			return fmt.Errorf("%s: step does nothing", at)
		default:
			return fmt.Errorf("%s: several actions in one step", at)
		}
	}
	return nil
}

// Branches returns the nested step sets: switch/if branches, the for_each body,
// parallel branches. Needed by anyone walking a description in full — validators
// and the linter: otherwise each of them re-enumerates the places nesting can
// occur and forgets whichever was added last (exactly how a switch branch would
// carry a typo into production).
func (s *Step) Branches() [][]Step {
	var out [][]Step
	if s.Switch != nil {
		for _, br := range s.Switch.Cases {
			out = append(out, br)
		}
		if len(s.Switch.Default) > 0 {
			out = append(out, s.Switch.Default)
		}
	}
	if s.If != nil {
		if len(s.If.Then) > 0 {
			out = append(out, s.If.Then)
		}
		if len(s.If.Else) > 0 {
			out = append(out, s.If.Else)
		}
	}
	if s.ForEach != nil && len(s.ForEach.Steps) > 0 {
		out = append(out, s.ForEach.Steps)
	}
	if s.Parallel != nil {
		out = append(out, s.Parallel.Branches...)
	}
	return out
}

// AnswerVar — the variable the turn's answer is taken from. A step that did not
// name a save_as writes here: not naming one is the ordinary way of saying "this
// is the final step".
const AnswerVar = "answer"

// BuiltinServer — the pseudo-server for the embedding application's built-in
// tools (`call: {tool: "builtin:run_script"}`).
//
// It exists so that a deterministic chain can be expressed with `call` steps
// instead of being handed to the model: the chain search_tickets →
// count_per_day → chart_timeseries is the same every time, and three model calls
// for it are three chances to mix up a handle or an argument.
const BuiltinServer = "builtin"

// RunnerFunc and ToolCallerFunc — functions as implementations of the
// interfaces.
//
// An embedder usually has nothing to keep in a struct: executing a step means
// calling their model, calling a tool means using their transport. Requiring a
// type declaration for that is requiring ceremony.
type RunnerFunc func(ctx context.Context, req StepRequest) (Result, error)

func (f RunnerFunc) Run(ctx context.Context, req StepRequest) (Result, error) { return f(ctx, req) }

type ToolCallerFunc func(ctx context.Context, server, tool string, args map[string]any) (string, error)

func (f ToolCallerFunc) CallTool(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	return f(ctx, server, tool, args)
}
