package main

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	se "github.com/inhuman/skill-engine"
)

// runner is the whole adapter: skill-engine's Runner on top of any eino chat
// model. Everything eino-shaped in this example lives in these forty lines.
//
// The direction matters. The engine does not know eino, and eino does not know
// the engine — the application owns the seam between them, which is exactly
// what makes the engine embeddable in an application built on some other
// framework tomorrow.
type runner struct {
	chat model.BaseChatModel
	log  io.Writer
}

func (r runner) Run(ctx context.Context, req se.StepRequest) (se.Result, error) {
	// What the SKILL declared, translated into what eino calls it. A field the
	// skill sets and the executor drops is decoration: the author writes
	// `sampling: {temperature: 0}` on a classifier for a reason, and nothing
	// downstream tells them it never arrived.
	var opts []model.Option
	if req.Model != "" {
		opts = append(opts, model.WithModel(req.Model))
	}
	if s := req.Sampling; s != nil {
		if s.Temperature != nil {
			opts = append(opts, model.WithTemperature(*s.Temperature))
		}
		if s.TopP != nil {
			opts = append(opts, model.WithTopP(*s.TopP))
		}
		if s.MaxTokens != nil {
			opts = append(opts, model.WithMaxTokens(*s.MaxTokens))
		}
	}

	// The step's radius. An EMPTY list is not "no preference" — it is the
	// skill's guard, and a step that was handed no tools must be given none
	// here either. Binding tools "just in case" is how a prohibition quietly
	// stops being one.
	if len(req.Tools) > 0 {
		fmt.Fprintf(r.log, "  (step %q may use: %v)\n", req.Name, req.Tools)
		// A real application resolves these names to eino tools and passes them
		// with model.WithTools, or hands the step to an eino agent. This example
		// keeps `call` steps for the deterministic work and leaves the model to
		// think, which is the cheaper shape anyway.
	}

	msg, err := r.chat.Generate(ctx, []*schema.Message{schema.UserMessage(req.Instruction)}, opts...)
	if err != nil {
		return se.Result{}, err
	}

	// A truncated generation is not an empty one, and the engine cannot tell
	// them apart from the text. Whatever the executor KNOWS goes back in the
	// Result — that is what a step is marked degraded on.
	res := se.Result{Text: msg.Content}
	if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason == "length" {
		res.Truncated = true
		res.Note = "the model stopped at the token ceiling"
	}
	return res, nil
}
