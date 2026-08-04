package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// stubModel — an OpenAI-compatible endpoint that answers whatever it is told
// to. The example is verified end to end against it: an example that only
// compiles teaches nothing about whether it works.
func stubModel(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q — the endpoint is not the one an OpenAI-compatible server exposes", r.URL.Path)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the request is not JSON: %v", err)
		}
		if len(body.Messages) == 0 {
			t.Error("the instruction never reached the model")
		}
		// Charged by length, the way a real endpoint charges. That is what makes
		// the A/B in QUICKSTART.md checkable without a model: the prose half
		// carries its whole dictionary into the prompt, the steps half does not.
		prompt := 0
		for _, m := range body.Messages {
			prompt += len(m.Content) / 4
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": answer},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens":     prompt,
				"completion_tokens": len(answer) / 4,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withStub(t *testing.T, answer string) {
	t.Helper()
	srv := stubModel(t, answer)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "not-a-real-key")
	t.Setenv("OPENAI_MODEL", "stub")
}

// The whole example, on a skill shipped in ../skills: conditions pick the
// sections the request names, `call` steps fetch them without a model, and the
// last step words the answer.
func TestExampleRunsAShippedSkill(t *testing.T) {
	withStub(t, "Тирамису и чай — 30 минут.")

	var out bytes.Buffer
	if err := run("../skills/menu.yaml", "подбери десерт и напиток", "", &out); err != nil {
		t.Fatalf("the example does not run: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"Тирамису и чай",      // the model's answer came back as the turn's answer
		"call recipes:search", // a `call` step ran without a model
		"pick_dessert",        // the trace names the steps
		"answer",              //
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the output does not mention %q:\n%s", want, got)
		}
	}
	// A section the request never named must not be FETCHED — but it must still
	// be visible as skipped. "we did not go there" and "it came back empty" are
	// different answers, and the trace is where the difference survives.
	if n := strings.Count(got, "call recipes:search"); n != 2 {
		t.Errorf("expected two sections fetched, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "skipped: nothing_named, pick_main") {
		t.Errorf("the skipped steps are not reported:\n%s", got)
	}
}

// A request naming no section leaves the skill through `exit`, and that is not
// a failure: the turn goes back to its ordinary path. An embedder that treats
// it as an error reports a bug where the skill made a decision.
func TestExampleHandlesAnExit(t *testing.T) {
	withStub(t, "unused")

	var out bytes.Buffer
	if err := run("../skills/menu.yaml", "расскажи что-нибудь", "", &out); err != nil {
		t.Fatalf("an exit was reported as a failure: %v", err)
	}
	if !strings.Contains(out.String(), "stepped aside") {
		t.Errorf("the exit was not recognised:\n%s", out.String())
	}
}

// Every skill in ../skills must at least load and validate through the same
// path the application uses. A shipped example that stopped parsing teaches the
// wrong thing, and this is the cheapest place to notice.
func TestEveryShippedSkillLoads(t *testing.T) {
	withStub(t, "answer")

	for _, path := range shippedSkills(t) {
		t.Run(path, func(t *testing.T) {
			var out bytes.Buffer
			err := run(path, "подбери десерт", "", &out)
			// Running is allowed to fail — most of these skills need tools this
			// example does not implement. Loading is not.
			if err != nil && strings.Contains(err.Error(), "skill-engine:") {
				t.Fatalf("the skill did not load: %v", err)
			}
		})
	}
}

// shippedSkills lists the skill files next door, minus the vocabulary of values
// (it has no name and is not a skill).
func shippedSkills(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob(filepath.Join("..", "skills", "*.yaml"))
	if err != nil || len(all) == 0 {
		t.Fatalf("the skills are gone: %v", err)
	}
	var out []string
	for _, path := range all {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if doc.Name != "" {
			out = append(out, path)
		}
	}
	return out
}

// The A/B QUICKSTART.md walks a reader through: one skill carrying both
// descriptions, the same request, one flag changed.
//
// What is checked here is the mechanism and the direction, not a benchmark —
// the stub charges by prompt length, so the numbers are the shape of the real
// ones rather than the real ones. In prose the dictionary of sections rides
// into the prompt and the model has to apply it; in steps the conditions
// applied it before any generation happened, and the prompt carries only what
// was found.
func TestSameSkillBothWays(t *testing.T) {
	withStub(t, "Тирамису и чай.")

	var prose, steps bytes.Buffer
	if err := run("../skills/menu.yaml", "подбери десерт и напиток", "playbook", &prose); err != nil {
		t.Fatalf("the prose half does not run: %v", err)
	}
	if err := run("../skills/menu.yaml", "подбери десерт и напиток", "workflow", &steps); err != nil {
		t.Fatalf("the steps half does not run: %v", err)
	}

	for _, out := range []*bytes.Buffer{&prose, &steps} {
		if !strings.Contains(out.String(), "=== cost (") {
			t.Fatalf("the run did not report what it cost:\n%s", out.String())
		}
	}
	if p, s := promptTokens(t, &prose), promptTokens(t, &steps); s >= p {
		t.Errorf("steps cost %d prompt tokens against prose %d — the dictionary should not reach the model at all", s, p)
	}

	// And the part a token count cannot show: in steps the sections are chosen
	// before any generation, so a section nobody named cannot be fetched. In
	// prose that is a request, and requests are followed probabilistically.
	if !strings.Contains(steps.String(), "skipped: nothing_named, pick_main") {
		t.Errorf("the steps half did not decide the sections deterministically:\n%s", steps.String())
	}
}

func promptTokens(t *testing.T, out *bytes.Buffer) int {
	t.Helper()
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.Contains(line, "prompt + ") {
			continue
		}
		var p, c, total int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "tokens: %d prompt + %d completion = %d", &p, &c, &total); err != nil {
			t.Fatalf("cannot read the cost line %q: %v", line, err)
		}
		return p
	}
	t.Fatalf("no cost line in:\n%s", out.String())
	return 0
}
