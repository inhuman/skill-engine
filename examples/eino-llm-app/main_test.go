package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// stubChat implements eino's chat model interface and records what it was
// asked. Because the adapter takes the INTERFACE rather than a vendor's client,
// the example can be verified end to end with no network and no key — and so
// can yours.
type stubChat struct {
	answer string
	seen   []*schema.Message
	opts   int
}

func (s *stubChat) Generate(_ context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	s.seen = append(s.seen, in...)
	s.opts = len(opts)
	return schema.AssistantMessage(s.answer, nil), nil
}

func (s *stubChat) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("this example only uses Generate")
}

// The whole example against a shipped skill: conditions choose the sections the
// request names, `call` steps fetch them with no model involved, and the last
// step words the answer through eino.
func TestExampleRunsAShippedSkill(t *testing.T) {
	chat := &stubChat{answer: "Тирамису и чай — 30 минут."}

	var out bytes.Buffer
	if err := run(chat, "../skills/menu.yaml", "подбери десерт и напиток", &out); err != nil {
		t.Fatalf("the example does not run: %v", err)
	}
	got := out.String()

	for _, want := range []string{"Тирамису и чай", "call recipes:search", "pick_dessert"} {
		if !strings.Contains(got, want) {
			t.Errorf("the output does not mention %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "call recipes:search"); n != 2 {
		t.Errorf("expected two sections fetched, got %d:\n%s", n, got)
	}
	if len(chat.seen) != 1 {
		t.Fatalf("expected one generation, got %d", len(chat.seen))
	}
	if !strings.Contains(chat.seen[0].Content, "Тирамису") {
		t.Errorf("what the tools returned never reached the model:\n%s", chat.seen[0].Content)
	}
}

// What a skill declares must arrive at the model. A step's `model:` and
// `sampling:` are translated into eino options; an adapter that drops them
// turns those fields into decoration, and nothing tells the author.
func TestDeclarationsReachTheModel(t *testing.T) {
	chat := &stubChat{answer: "ok"}
	var out bytes.Buffer

	if err := run(chat, "testdata/declared.yaml", "что угодно", &out); err != nil {
		t.Fatalf("the example does not run: %v", err)
	}
	// model + temperature + max_tokens
	if chat.opts != 3 {
		t.Errorf("expected three options passed to eino, got %d", chat.opts)
	}
}

// A skill that leaves through `exit` did not fail: it decided the request was
// not its case. An embedder reporting that as an error reports a bug where the
// skill made a decision.
func TestExampleHandlesAnExit(t *testing.T) {
	chat := &stubChat{answer: "unused"}

	var out bytes.Buffer
	if err := run(chat, "../skills/menu.yaml", "расскажи что-нибудь", &out); err != nil {
		t.Fatalf("an exit was reported as a failure: %v", err)
	}
	if !strings.Contains(out.String(), "stepped aside") {
		t.Errorf("the exit was not recognised:\n%s", out.String())
	}
}
