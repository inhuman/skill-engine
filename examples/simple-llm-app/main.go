// A minimal application that embeds the skill engine.
//
// It shows the whole contract in one file: load a skill, hand the engine the
// four things it cannot do itself — talk to a model, call a tool, resolve an
// asset, read working memory — and print what came back.
//
// The model is reached over the OpenAI-compatible /chat/completions endpoint,
// which is what vLLM, Ollama, LM Studio and the hosted APIs all speak, using
// nothing but net/http. That keeps this example honest about the engine's own
// promise: it adds no dependencies of its own, and neither does using it.
//
//	go run . -skill ../skills/menu.yaml -input "подбери десерт и напиток"
//
// Set OPENAI_BASE_URL and OPENAI_API_KEY for your endpoint. There is no key in
// the code and no default that quietly points somewhere.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

func main() {
	skillPath := flag.String("skill", "../skills/menu.yaml", "path to a skill file")
	input := flag.String("input", "подбери десерт и напиток", "the user's request")
	// Overrides the skill's own `mode`. A skill carrying BOTH descriptions can
	// then be run either way on the same request — which is the A/B in
	// QUICKSTART.md, and the reason `mode` exists in the format at all.
	mode := flag.String("mode", "", "run the skill as `workflow` or `playbook` (default: what the skill says)")
	flag.Parse()

	if err := run(*skillPath, *input, *mode, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(skillPath, input, forceMode string, out io.Writer) error {
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return err
	}

	// One call reads the whole file — header and description alike. Validate is
	// separate on purpose: "this is not YAML" and "this YAML says something
	// wrong" are different answers, and a tool reporting on a file needs to
	// tell them apart.
	skill, err := se.ParseSkill(raw, yaml.Unmarshal)
	if err != nil {
		return err
	}
	if err := skill.Validate(); err != nil {
		return err
	}

	// Which of the two descriptions to run. A skill may carry a `playbook` as
	// well, and then the ENGINE takes no part: the application runs the prompt
	// itself. Handling that case is what makes an embedder complete.
	//
	// An explicit mode with an empty half is an ERROR rather than a fallback to
	// the other one, and the engine enforces that: a silent fallback would give
	// a clean run over the description that was NOT selected, and a conclusion
	// drawn from a turn the chosen half never took part in.
	declared := skill.Mode
	if forceMode != "" {
		declared = forceMode
	}
	mode, err := se.ResolveMode(declared, skill.HasWorkflow(), skill.HasPlaybook())
	if err != nil {
		return err
	}

	st := &stats{}
	if mode == se.ModePlaybook {
		// The whole task in one prompt: the model decides what to look up, calls
		// what it decides, and words the answer. The engine takes no part.
		answer, u, err := newModel().complete(context.Background(), skill.Playbook+"\n\nЗапрос: "+input, nil)
		if err != nil {
			return err
		}
		st.add(u)
		fmt.Fprintln(out, "\n=== answer ===")
		fmt.Fprintln(out, answer)
		st.report(out, "playbook")
		return nil
	}

	vars, outcome, err := se.ExecuteWith(context.Background(), skill.Workflow, deps(out, st), map[string]string{
		"input": input,
	})
	if err != nil {
		// A skill leaving on purpose is not a failure: it decided the request
		// was not its case, and the turn goes back to its ordinary path.
		if errors.Is(err, se.ErrExit) {
			fmt.Fprintln(out, "the skill stepped aside:", err)
			return nil
		}
		return err
	}

	fmt.Fprintln(out, "\n=== answer ===")
	fmt.Fprintln(out, vars[se.AnswerVar])
	fmt.Fprintln(out, "\n=== steps ===")
	for _, s := range outcome.Steps {
		line := fmt.Sprintf("  %-16s %-11s %-8s calls=%d", s.Name, s.Kind, s.Outcome, s.Calls)
		if s.Reason != "" {
			line += "  " + s.Reason
		}
		fmt.Fprintln(out, line)
	}
	if len(outcome.Skipped) > 0 {
		fmt.Fprintln(out, "  skipped:", strings.Join(outcome.Skipped, ", "))
	}
	st.report(out, "workflow")
	return nil
}

// stats — what the turn cost. Counted because "steps are cheaper" is a claim
// until somebody puts a number next to it, and the same skill run both ways on
// the same request is the cheapest way to get one.
type stats struct {
	generations int
	prompt      int
	completion  int
}

func (s *stats) add(u usage) {
	s.generations++
	s.prompt += u.PromptTokens
	s.completion += u.CompletionTokens
}

func (s *stats) report(out io.Writer, mode string) {
	fmt.Fprintf(out, "\n=== cost (%s) ===\n", mode)
	fmt.Fprintf(out, "  generations: %d\n", s.generations)
	fmt.Fprintf(out, "  tokens:      %d prompt + %d completion = %d\n",
		s.prompt, s.completion, s.prompt+s.completion)
}

// deps is the whole contract between an application and the engine.
//
// Everything the engine cannot know — how to reach a model, what a tool is,
// where an asset's content lives, what your host calls things — arrives here.
// Nothing else is injected, and the engine logs nothing, stores nothing and
// reaches nowhere on its own.
func deps(out io.Writer, st *stats) se.Deps {
	m := newModel()
	return se.Deps{
		Runner:   runner{model: m, log: out, stats: st},
		Caller:   tools{log: out},
		Assets:   assets{},
		Delegate: nil, // no composite skills here: a `delegate` step would fail loudly
		Memory:   memory{},

		// The words of THIS application. The engine ships none — an agent about
		// a kitchen and one about a car fleet share the format, not a language.
		Vocabulary: se.Vocabulary{
			DecisionMarkers: []string{"Result:", "Answer:", "Итог:", "Ответ:"},
			TruncationNotes: []string{"shortened:"},
		},

		// A step that runs for ten seconds emits nothing until it finishes, and
		// there is nothing to show a human all that time.
		OnStepStart: func(name, kind string) {
			fmt.Fprintf(out, "→ %s (%s)\n", name, kind)
		},
	}
}

// runner executes an instruction step: it is the ONE place the engine touches a
// model.
//
// The step arrives fully resolved — variables substituted, assets inlined, the
// tool set narrowed, the sampling decided. All that is left is to generate.
type runner struct {
	model *openAI
	log   io.Writer
	stats *stats
}

func (r runner) Run(ctx context.Context, req se.StepRequest) (se.Result, error) {
	// The tool set is the step's radius, and an EMPTY one is not "no
	// preference": the step is meant to answer from what has already been
	// gathered. Handing it tools anyway would undo the guard the skill relies
	// on — so it is passed through as it came.
	if len(req.Tools) > 0 {
		fmt.Fprintf(r.log, "  (step %q may use: %s)\n", req.Name, strings.Join(req.Tools, ", "))
	}

	text, u, err := r.model.complete(ctx, req.Instruction, &req)
	if err != nil {
		return se.Result{}, err
	}
	r.stats.add(u)

	// Result carries more than the text: what the executor KNOWS and the engine
	// cannot derive. A truncated generation and a step that simply had nothing
	// to say look identical from the outside, and the engine marks a step
	// degraded on the strength of these fields.
	return se.Result{Text: text}, nil
}

// tools executes a `call` step — a tool invocation with no model involved.
//
// In a real application this is your MCP client, your HTTP client, your
// function registry. Here it is a table, so the example runs offline and every
// skill in ../skills can be tried out.
type tools struct{ log io.Writer }

func (t tools) CallTool(_ context.Context, server, tool string, args map[string]any) (string, error) {
	fmt.Fprintf(t.log, "  call %s:%s %v\n", server, tool, args)

	switch server + ":" + tool {
	case "recipes:search":
		return "1. Тирамису — 30 минут\n2. Панна-котта — 15 минут", nil
	}
	// A tool that does not exist must FAIL, not return an apology: the skill's
	// on_error policy decides what happens next, and it can only decide if it
	// is told.
	return "", fmt.Errorf("no such tool: %s:%s", server, tool)
}

// assets resolve the payloads a skill declares.
//
// `source: inline` is the engine's own case and still comes through here — the
// engine deliberately does not read `content` itself, because an asset is the
// application's to fetch, cache and police. Every other source is yours: a
// repository, a URL, a file the user uploaded.
type assets struct{}

func (assets) Resolve(_ context.Context, name string, a se.Asset) (string, error) {
	if a.Source == "inline" || a.Content != "" {
		return a.Content, nil
	}
	return "", fmt.Errorf("asset %q: source %q is not implemented in this example", name, a.Source)
}

// memory returns a large result by the handle the host appended to it.
//
// This example never truncates anything, so the map stays empty and the engine
// simply never asks. It is here to show WHERE the seam is: without a reader, a
// step that cannot fetch the rest of a value is told so in its trace instead of
// quietly working on a fragment.
type memory map[string]string

func (m memory) Get(id string) (string, bool) { v, ok := m[id]; return v, ok }
