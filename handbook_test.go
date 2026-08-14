package skillengine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	se "github.com/inhuman/skill-engine"
	"gopkg.in/yaml.v3"
)

// The handbook must ARRIVE, which is the whole reason it moved out of the
// ignored spec tree: an embedder gets this package through `go mod vendor`,
// and vendor copies only what the build references.
func TestHandbookIsEmbedded(t *testing.T) {
	index := se.HandbookIndex()
	require.NotEmpty(t, index, "the handbook did not travel with the package")

	onDisk, err := filepath.Glob("handbook/[0-9]*.md")
	require.NoError(t, err)
	assert.Len(t, index, len(onDisk), "a section on disk is missing from the index, or the other way round")

	for _, s := range index {
		assert.NotEmpty(t, se.Handbook(s.ID), "section %q is indexed and empty", s.ID)
	}
	assert.Empty(t, se.Handbook("no-such-section"),
		"an unknown id must yield nothing rather than the nearest match")
	assert.Empty(t, se.Handbook("README"), "the table of contents is not a section")
}

// The index is what travels in EVERY prompt, so it has to stay cheap: the point
// of splitting the handbook by section is that the whole of it (70 KB) does not
// go into a step. An index that grows into a document defeats that.
func TestHandbookIndexIsCheap(t *testing.T) {
	total := 0
	for _, s := range se.HandbookIndex() {
		assert.NotEmpty(t, s.Title, "section %q has no heading", s.ID)
		require.NotEmptyf(t, s.Summary, "section %q has no `> summary` line under its heading", s.ID)
		assert.LessOrEqualf(t, len([]rune(s.Summary)), 120,
			"the summary of %q is a paragraph; the index needs one line", s.ID)
		total += len(s.ID) + len(s.Title) + len(s.Summary)
	}
	assert.Less(t, total, 3000, "the index stopped being something you can put in a prompt whole")
}

// An id is what a refusal prints, so it must not move when a section is
// inserted before another: the number in the file name orders the files and is
// not part of the name.
func TestHandbookIDsAreStableAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range se.HandbookIndex() {
		assert.NotContains(t, s.ID, ".md")
		assert.Regexp(t, `^[a-z][a-z0-9-]*$`, s.ID)
		assert.Falsef(t, seen[s.ID], "two sections share the id %q", s.ID)
		seen[s.ID] = true
	}
}

// Every section opens with a FORM — a piece to copy, not an explanation of how
// to write one. Paid for by measurement: the prose hint "if you need a subset,
// make it a separate step" worked 0 times out of 16, while an example from the
// catalogue gets copied.
func TestEverySectionOpensWithAForm(t *testing.T) {
	for _, s := range se.HandbookIndex() {
		text := se.Handbook(s.ID)
		head, _, _ := strings.Cut(text, "\n---\n")
		assert.Containsf(t, head, "## Форма", "section %q does not open with a form", s.ID)
		assert.Containsf(t, head, "```", "the form of %q has nothing to copy", s.ID)
	}
}

// schemaKeyword — what may appear as `name:` in the handbook without being a
// field of the skill format: the JSON-Schema vocabulary a response_schema is
// written in, and the two argument conventions the format documents in prose
// rather than as properties.
var schemaKeyword = map[string]bool{
	"type": true, "properties": true, "required": true, "enum": true,
	"items": true, "maxlength": true, "maxitems": true, "minitems": true,
	"additionalproperties": true, "format": true, "default": true,
	"description": true, "from": true, "stdin": true, "code": true,
}

// fieldRe — a claimed field of the format: `on_error: continue` inside
// backticks, taken up to the first colon, with what follows it.
var fieldRe = regexp.MustCompile("`([a-z_][a-z0-9_]*):\\s*([^`]*)")

// declaresAType — `id: integer`, `strengths: items: {type: string}`. A name
// followed by a JSON-Schema type is a field of the SKILL AUTHOR's own
// response_schema, invented for one example, and the format knows nothing about
// it. A name followed by a value (`on_error: continue`, `tools: []`) is a field
// of the format being used, and that is what this guard is about.
var declaresAType = regexp.MustCompile(`^(integer|boolean|string|number|array|object|enum|items|required|\{type)\b`)

// The handbook must not become a SECOND source of truth about the format.
//
// Names and types of fields live in the schema and only there: a handbook that
// lists fields diverges from it on the first change, and the reader cannot tell
// which of the two is lying. Its subject is USE and failure classes — exactly
// what the schema has no room for.
//
// So: a section naming a field must take the name from the schema, or not name
// it. This is the test that keeps that true.
func TestHandbookNamesOnlyFieldsTheSchemaHas(t *testing.T) {
	known := schemaFieldNames(t)
	for _, s := range se.HandbookIndex() {
		for _, m := range fieldRe.FindAllStringSubmatch(se.Handbook(s.ID), -1) {
			name := m[1]
			if schemaKeyword[strings.ToLower(name)] || declaresAType.MatchString(strings.TrimSpace(m[2])) {
				continue
			}
			assert.Truef(t, known[name],
				"section %q writes `%s:` and the schema has no such field — "+
					"either the name is wrong or the schema is the one that changed", s.ID, name)
		}
	}
}

// schemaFieldNames — every property name the schema defines, at any level.
func schemaFieldNames(t *testing.T) map[string]bool {
	t.Helper()
	var doc any
	require.NoError(t, yaml.Unmarshal([]byte(se.SchemaYAML), &doc))

	out := map[string]bool{}
	var walk func(node any, underProperties bool)
	walk = func(node any, underProperties bool) {
		switch v := node.(type) {
		case map[string]any:
			for k, nested := range v {
				if underProperties {
					out[k] = true
				}
				walk(nested, k == "properties")
			}
		case []any:
			for _, nested := range v {
				walk(nested, false)
			}
		}
	}
	walk(doc, false)
	require.NotEmpty(t, out)
	return out
}

// privateNames — the installation's own names, read from the list the git hooks
// share.
//
// NOT spelled out here, and that is the whole point: this file is public, so a
// list of private names inside it would publish exactly what it exists to keep
// out. The repository had already decided that — `.githooks/private-names` says
// so in its own header — and the pre-commit hook refuses any tracked file that
// writes such a name, this one included. It fired on the first version of this
// test, which is how the list got here in the first place.
//
// So there is one copy, in a directory that is gitignored, and this reads it.
// Only the NAMES, not the shape heuristics beside them: those are tuned for the
// added lines of a commit, and over whole files they fire on an e-mail in a
// licence or a hostname in prose — a check that always fires is one people
// switch off.
//
// Without the file (a fresh clone, CI) there is nothing to check against, and
// the test says so rather than passing quietly. The authoritative check is the
// hook: `git config core.hooksPath .githooks`.
func privateNames(t *testing.T) *regexp.Regexp {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".githooks", "private-names"))
	if err != nil {
		t.Skip("нет .githooks/private-names — список приватных имён живёт только там; " +
			"включается через git config core.hooksPath .githooks")
	}
	var parts []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PRIVATE_NAMES=") {
			continue
		}
		body := strings.Trim(strings.TrimPrefix(line, "PRIVATE_NAMES="), `'"`)
		body = strings.TrimPrefix(body, "$PRIVATE_NAMES|")
		parts = append(parts, body)
	}
	require.NotEmpty(t, parts, "список приватных имён пуст — сторож охранял бы пустоту")
	re, err := regexp.Compile("(?i)" + strings.Join(parts, "|"))
	require.NoError(t, err)
	return re
}

// publicFiles — the files that will actually SHIP: what git tracks, plus what is
// untracked and not ignored.
//
// Asking git rather than walking the tree is not convenience, it is the
// definition. The first version of this guard walked everything and reported
// `specs/` — a directory that is in .gitignore and goes nowhere, holding
// working copies of the consuming application's sources. A guard that names
// files nobody publishes is a guard that gets switched off.
func publicFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Skip("git недоступен — списка публикуемых файлов взять неоткуда")
	}
	var files []string
	for _, path := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch filepath.Ext(path) {
		case ".go", ".md", ".yaml", ".yml":
			// This file carries both lists as data; it cannot be its own subject.
			if filepath.Base(path) != "handbook_test.go" {
				files = append(files, path)
			}
		}
	}
	require.NotEmpty(t, files)
	return files
}

// Публичный файл не имеет права называть чужую установку.
//
// Шире хука по охвату и уже по времени: хук смотрит ДОБАВЛЕННЫЕ строки коммита,
// этот тест — целые файлы, то есть находит и то, что доехало раньше, чем список
// пополнился. Так и нашлись `gitlab-write-prod` и `k8s-job` в фикстурах.
func TestNoPublicFileNamesAnInstallation(t *testing.T) {
	private := privateNames(t)
	for _, path := range publicFiles(t) {
		text, err := os.ReadFile(path)
		if err != nil {
			continue // удалён между листингом и чтением — не наше дело
		}
		for _, hit := range private.FindAllIndex(text, -1) {
			// Своя печать вместо assert.Contains: тот при промахе вываливает
			// ВЕСЬ файл, и одна находка залила бы вывод так, что остальных в
			// нём не разглядеть. Имя не печатается — оно приватное; печатается
			// адрес, по которому его видно.
			t.Errorf("%s:%d называет имя из .githooks/private-names — это существует ровно в одной установке",
				path, strings.Count(string(text[:hit[0]]), "\n")+1)
		}
	}
}

// hostVocabulary — words that belong to ONE installation and must not ride into
// the module with the handbook.
//
// Five published versions of this library were retracted for exactly this: they
// carried the vocabulary of the installation the format grew in — word lists
// baked into the engine and the linter, an example built from one application's
// dictionary. The handbook arrived from that same installation, and it named
// that application's tools, telemetry fields, clusters and skills as though
// they were the format's.
//
// The list is short and specific on purpose. It is not a filter for prose about
// products (a live case is allowed to say it happened in a tracker), and it
// holds no private NAMES — those come from the hooks' own list, see
// privateNames above. What is left is STYLE: words that read as written inside
// one deployment, and that reappear when the handbook is re-synced from there.
var hostVocabulary = []string{
	"mcp_call", "mcp_list_servers", "mcp-exec",
	"skill_step", "skill_program", "subagent_llm_call", "content_excerpt", "content_tail",
	"GRAMMAR_CAPABLE_MODELS", "vllm/",
	"шейкдаун", "батаре", "принципал", "субагент", "оркестратор",
}

func TestHandbookCarriesNoInstallationVocabulary(t *testing.T) {
	files, err := filepath.Glob("handbook/*.md")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, path := range files {
		text, err := os.ReadFile(path)
		require.NoError(t, err)
		low := strings.ToLower(string(text))
		for _, word := range hostVocabulary {
			assert.NotContainsf(t, low, strings.ToLower(word),
				"%s carries %q — a name that means something in one installation only", path, word)
		}
	}
}
