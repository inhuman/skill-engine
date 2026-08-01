package skillengine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Assets are named payloads a skill hands to a tool BY REFERENCE, without
// passing them through the model's context.
//
// Why: a model cannot reproduce a multi-kilobyte literal verbatim through a
// tool argument — it REGENERATES and corrupts it (live failure: six
// SyntaxErrors in a row on a rendering script, then a hardcoded surrender).
//
// Described along TWO independent axes. They are deliberately not collapsed
// into one enum: that would give combinatorics (code_inline, code_repo,
// config_mcp…), and every new source would need as many values as there are
// kinds.

// The asset kind, its source and its delivery route are the APPLICATION's
// vocabulary, not the engine's.
//
// The engine does not know what kinds of payload exist, where they are taken
// from, or where the output goes. It knows one thing: the content is either
// right here or reachable at an address. Everything else is handled by the
// embedder's resolver — hence plain strings in the declaration rather than
// enums.

// Asset — a payload declared in a skill.
type Asset struct {
	Kind   string `yaml:"kind,omitempty"`
	Source string `yaml:"source,omitempty"`
	// Content — the content itself, for source: inline.
	Content string `yaml:"content,omitempty"`
	// Ref — the address for external sources: "project@branch:path" |
	// "path in file storage" | "server:tool".
	Ref string `yaml:"ref,omitempty"`
	// Args — MCP call arguments for source: mcp. They support {{var}}.
	Args map[string]any `yaml:"args,omitempty"`
	// Params — whatever only makes sense for a SPECIFIC kind: the language for
	// code (giving a linter a reason to check syntax before production), the
	// format for text, the schema for data.
	//
	// A map rather than struct fields, because kinds are the APPLICATION's
	// vocabulary and the engine does not know them (see Kind above). A field
	// per kind would mean the library grows with every foreign kind while
	// sitting empty on every other asset: that is exactly how `lang` lived
	// here, needed by one kind out of four.
	//
	// The engine does not look inside and does not check the shape — it hands
	// this to the resolver as is.
	Params map[string]any `yaml:"params,omitempty"`
	// Deliver — where the OUTPUT of the tool that consumed the asset goes:
	// reply — the output becomes the turn's answer; file — delivered as a
	// file; empty — returned to the step.
	//
	// It exists because the model forgets to attach the fragile delivery
	// argument (live failure: the render worked, the file never reached the
	// user). The skill author declares the route, the bridge pins it down.
	Deliver string `yaml:"deliver,omitempty"`
	// Description — what the asset is for; read by the HUMAN editing the
	// skill. Especially needed for external ones: their content is not visible
	// in the file.
	Description string `yaml:"description,omitempty"`
	// Fetch — the retrieval policy for external sources.
	Fetch *Fetch `yaml:"fetch,omitempty"`
}

// Fetch — how an external asset is retrieved.
//
// An external asset is fetched DURING the turn rather than from a periodic
// cache: the point of an external source is freshness. A wiki page gets
// edited, a service list changes; handing out yesterday's copy cancels the
// reason the asset was made external. The cost is network on the hot path, and
// it is treated with this policy rather than a ban.
type Fetch struct {
	// TTL — how long the content counts as fresh. Empty/0 — fetch every time.
	TTL time.Duration `yaml:"ttl,omitempty"`
	// Timeout — the ceiling on ONE attempt. A turn must not hang on someone
	// else's downtime.
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// Retries — retries beyond the first attempt, with exponential backoff.
	// Only transient failures are retried: retrying a 404 will not change the
	// outcome.
	Retries int `yaml:"retries,omitempty"`
	// OnUnavailable — what to do when retrieval failed:
	// fail (default) | stale — the previous copy | empty — nothing.
	OnUnavailable string `yaml:"on_unavailable,omitempty"`
}

// AssetResolver fetches an asset's content. The domain (repositories, file
// storage, MCP) lives behind the interface — the engine knows nothing of it.
type AssetResolver interface {
	Resolve(ctx context.Context, name string, a Asset) (string, error)
}

// validateAssets checks declarations before execution: field pairings and that
// the reference leads somewhere at all.
func validateAssets(assets map[string]Asset) error {
	for name, a := range assets {
		hasContent := strings.TrimSpace(a.Content) != ""
		hasRef := strings.TrimSpace(a.Ref) != ""
		switch {
		case !hasContent && !hasRef:
			return fmt.Errorf("asset %q: neither content nor ref — nothing to execute", name)
		case hasContent && hasRef:
			return fmt.Errorf("asset %q: both content and ref set — unclear which to take", name)
		}
		if a.Fetch != nil {
			switch a.Fetch.OnUnavailable {
			case "", "fail", "stale", "empty":
			default:
				return fmt.Errorf("asset %q: unknown on_unavailable %q", name, a.Fetch.OnUnavailable)
			}
		}
	}
	return nil
}

// AssetRefsInText lists assets substituted into text ({{asset:name}}).
func AssetRefsInText(text string) []string {
	matches := assetRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// AssetRefsInArgs lists assets passed by reference ({from: "asset:name"}) —
// that is, past the model's context. The difference from AssetRefsInText is
// not stylistic: it decides whether the model sees the content.
func AssetRefsInArgs(args map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if from, ok := t["from"].(string); ok && len(t) == 1 {
				if name, isAsset := strings.CutPrefix(from, "asset:"); isAsset {
					out = append(out, name)
					return
				}
			}
			for _, nested := range t {
				walk(nested)
			}
		case []any:
			for _, nested := range t {
				walk(nested)
			}
		}
	}
	walk(args)
	return out
}
