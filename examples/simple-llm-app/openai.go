package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	se "github.com/inhuman/skill-engine"
)

// openAI — a chat completion over the OpenAI-compatible endpoint, in net/http.
//
// Sixty lines, no client library. That is not thrift for its own sake: the
// engine's promise is that embedding it adds no dependencies, and an example
// that pulls in an SDK to prove the point would disprove it.
type openAI struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func newModel() *openAI {
	return &openAI{
		baseURL: strings.TrimSuffix(env("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		apiKey:  os.Getenv("OPENAI_API_KEY"),
		model:   env("OPENAI_MODEL", "gpt-4o-mini"),
		client:  &http.Client{Timeout: 2 * time.Minute},
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// complete sends one instruction and returns the answer's text.
//
// req is the step as the engine resolved it, and it carries more than the
// prompt. Passing those fields on is what makes a step's declarations real:
// a `model:` the skill named, its `sampling:`, its `response_schema:`. An
// executor that ignores them turns every one of those fields into decoration —
// the skill declares, the engine forwards, and nothing happens.
func (o *openAI) complete(ctx context.Context, instruction string, req *se.StepRequest) (string, error) {
	body := map[string]any{
		"model":    o.model,
		"messages": []map[string]string{{"role": "user", "content": instruction}},
	}
	if req != nil {
		if req.Model != "" {
			body["model"] = req.Model
		}
		if s := req.Sampling; s != nil {
			if s.Temperature != nil {
				body["temperature"] = *s.Temperature
			}
			if s.TopP != nil {
				body["top_p"] = *s.TopP
			}
			if s.MaxTokens != nil {
				body["max_tokens"] = *s.MaxTokens
			}
		}
		// A structured answer is only structured where the decoding grammar
		// holds it. Dropping the schema here is the silent hole the format
		// warns about: the model "usually" answers JSON, and the step that
		// parses it fails on the day it does not.
		if len(req.ResponseSchema) > 0 {
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "step",
					"strict": true,
					"schema": req.ResponseSchema,
				},
			}
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message      struct{ Content string } `json:"message"`
			FinishReason string                   `json:"finish_reason"`
		} `json:"choices"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding the answer: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("model: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("model returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}
