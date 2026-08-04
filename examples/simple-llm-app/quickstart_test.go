package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The minimal skill QUICKSTART.md tells a reader to save and run. It is the
// first thing they will execute, so it is the first thing that must work.
func TestQuickstartMinimalSkill(t *testing.T) {
	const skill = `skill_engine_version: "2.2.0"
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
`
	path := filepath.Join(t.TempDir(), "my-skill.yaml")
	if err := os.WriteFile(path, []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	withStub(t, "Привет. Чем помочь?")

	var out bytes.Buffer
	if err := run(path, "привет", "", &out); err != nil {
		t.Fatalf("the skill QUICKSTART tells the reader to write does not run: %v", err)
	}
	if !strings.Contains(out.String(), "Чем помочь") {
		t.Errorf("no answer:\n%s", out.String())
	}
}
