package skillengine

// The skill author's handbook: failure classes and the forms that avoid them.

import (
	"embed"
	"path"
	"sort"
	"strings"
)

// handbookFS — the handbook, embedded the way the schema is.
//
// Files in the repository are not access. An embedder gets this package through
// `go mod vendor`, which copies only what the build references: a directory of
// markdown nobody imports simply does not travel, and a handbook that does not
// travel exists on one machine.
//
// The reason it has to travel at all is measured. Over three days of a model
// writing skills, eleven commits in a row were one class — a form the format
// does not have, a different one each time. Three measurements from those days
// say what does not fix it and what does:
//
//   - the LIST OF FIELDS in the prompt does not: invented keys 1 of 8 against
//     0 of 8, inside the noise, with refusals holding in both arms. The author
//     is not short of field NAMES;
//   - PROSE does not: "if you need a subset, make it a separate step" worked 0
//     times out of 16. A ready form gets copied; an explanation does not;
//   - a POINTER to knowledge works only where the addressee can go. The refusal
//     ended with "call the schema tool" — dead twice over on the program path,
//     since no skill had that tool in its radius and the steps that write
//     skills run with `tools: []`.
//
// Hence: whole forms, reachable by a tool. The index is a few hundred bytes and
// travels in every prompt; a section is fetched when it is needed.
//
//go:embed handbook/*.md
var handbookFS embed.FS

// HandbookSection — one section of the handbook in the index.
type HandbookSection struct {
	// ID — the name a refusal can point at: "response-schema", "failures".
	ID string
	// Title — the section's heading, as the section itself writes it.
	Title string
	// Summary — one line: what this section is about.
	Summary string
}

// HandbookIndex returns the sections, in reading order.
//
// Cheap on purpose — a few hundred bytes all together, so it can sit in a
// prompt whole without pushing the task out of it. The text of a section is
// fetched by ID with Handbook.
func HandbookIndex() []HandbookSection {
	files, err := handbookFS.ReadDir("handbook")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		if f.IsDir() || f.Name() == "README.md" {
			// README is the human's way in — the table of contents, not a
			// section. It says the same things the index does.
			continue
		}
		names = append(names, f.Name())
	}
	// By file name: the numeric prefix is the reading order, and it is the only
	// place that order is written down. A second list in Go would drift from
	// the directory on the first section added.
	sort.Strings(names)

	out := make([]HandbookSection, 0, len(names))
	for _, name := range names {
		text, err := handbookFS.ReadFile(path.Join("handbook", name))
		if err != nil {
			continue
		}
		title, summary := handbookHead(string(text))
		out = append(out, HandbookSection{ID: handbookID(name), Title: title, Summary: summary})
	}
	return out
}

// Handbook returns the text of one section. An unknown id yields an empty
// string — the caller asked for something that is not there, and inventing a
// nearest match would answer a question nobody asked.
func Handbook(id string) string {
	for _, f := range handbookFiles() {
		if handbookID(f) == id {
			text, err := handbookFS.ReadFile(path.Join("handbook", f))
			if err != nil {
				return ""
			}
			return string(text)
		}
	}
	return ""
}

func handbookFiles() []string {
	files, err := handbookFS.ReadDir("handbook")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if !f.IsDir() && f.Name() != "README.md" {
			out = append(out, f.Name())
		}
	}
	sort.Strings(out)
	return out
}

// handbookID turns a file name into the id a refusal can print:
// "01-response-schema.md" → "response-schema". The number orders the files and
// is not part of the name — an id that changes when a section is inserted
// before it would break every refusal that quotes it.
func handbookID(file string) string {
	name := strings.TrimSuffix(file, ".md")
	if i := strings.Index(name, "-"); i > 0 && strings.Trim(name[:i], "0123456789") == "" {
		name = name[i+1:]
	}
	return name
}

// handbookHead reads the section's own first line and its one-line summary.
//
// The summary is the blockquote right under the heading, not the first
// paragraph: a lede is two or three sentences and belongs to the section, while
// the index needs one line per section and needs it to stay one line. A test
// keeps every section carrying both.
func handbookHead(text string) (title, summary string) {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case title == "" && strings.HasPrefix(line, "# "):
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case title != "" && strings.HasPrefix(line, "> "):
			return title, strings.TrimSpace(strings.TrimPrefix(line, "> "))
		case title != "" && strings.HasPrefix(line, "## "):
			return title, "" // the summary was missing; the test says so
		}
	}
	return title, ""
}
