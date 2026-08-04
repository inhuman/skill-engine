// The same application, with the model reached through eino.
//
// The point of this example is one type — the adapter below — and one fact
// about it: eino appears in THIS module's go.mod and nowhere else. The engine
// stays dependency-free, and an application built on a framework plugs the
// framework in at the only seam that touches a model.
//
//	go run . -skill ../skills/menu.yaml -input "подбери десерт и напиток"
//
// Set OPENAI_BASE_URL, OPENAI_API_KEY and OPENAI_MODEL for your endpoint; eino's
// OpenAI component speaks to anything OpenAI-compatible.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"gopkg.in/yaml.v3"

	se "github.com/inhuman/skill-engine"
)

func main() {
	skillPath := flag.String("skill", "../skills/menu.yaml", "path to a skill file")
	input := flag.String("input", "подбери десерт и напиток", "the user's request")
	flag.Parse()

	chat, err := newChatModel(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot build the model:", err)
		os.Exit(1)
	}
	if err := run(chat, *skillPath, *input, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newChatModel builds eino's OpenAI component. Any other implementation from
// eino-ext — Ark, Ollama, Qwen — drops in here without touching the adapter:
// the adapter knows the INTERFACE, not the vendor.
func newChatModel(ctx context.Context) (model.BaseChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   env("OPENAI_MODEL", "gpt-4o-mini"),
	})
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run(chat model.BaseChatModel, skillPath, input string, out io.Writer) error {
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return err
	}
	skill, err := se.ParseSkill(raw, yaml.Unmarshal)
	if err != nil {
		return err
	}
	if err := skill.Validate(); err != nil {
		return err
	}

	vars, outcome, err := se.ExecuteWith(context.Background(), skill.Workflow, se.Deps{
		Runner: runner{chat: chat, log: out},
		Caller: tools{log: out},
		Assets: assets{},
		Vocabulary: se.Vocabulary{
			DecisionMarkers: []string{"Result:", "Ответ:"},
		},
		OnStepStart: func(name, kind string) { fmt.Fprintf(out, "→ %s (%s)\n", name, kind) },
	}, map[string]string{"input": input})
	if err != nil {
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
		fmt.Fprintf(out, "  %-16s %-11s %-8s %s\n", s.Name, s.Kind, s.Outcome, s.Reason)
	}
	return nil
}

// tools and assets are the same as in ../simple-llm-app: eino changes how the
// MODEL is reached and nothing else about embedding the engine.
type tools struct{ log io.Writer }

func (t tools) CallTool(_ context.Context, server, tool string, args map[string]any) (string, error) {
	fmt.Fprintf(t.log, "  call %s:%s %v\n", server, tool, args)
	if server+":"+tool == "recipes:search" {
		return "1. Тирамису — 30 минут\n2. Панна-котта — 15 минут", nil
	}
	return "", fmt.Errorf("no such tool: %s:%s", server, tool)
}

type assets struct{}

func (assets) Resolve(_ context.Context, name string, a se.Asset) (string, error) {
	if a.Source == "inline" || a.Content != "" {
		return a.Content, nil
	}
	return "", fmt.Errorf("asset %q: source %q is not implemented in this example", name, a.Source)
}
