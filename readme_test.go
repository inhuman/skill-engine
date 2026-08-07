package skillengine

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two READMEs are one document in two languages, and they drift the moment
// somebody edits one of them. The titles cannot be compared — that is the whole
// point of having two — so what is compared is the SHAPE: how many sections, in
// what order by kind, and how many code samples.
//
// The same guard the schema translations already have, for the same reason: a
// reader of the Russian file must not be reading a version of the library that
// no longer exists.
func TestBothReadmesHaveTheSameShape(t *testing.T) {
	forEachTranslatedPair(t, "README.md", "README.ru.md")
	forEachTranslatedPair(t, "QUICKSTART.md", "QUICKSTART.ru.md")
}

func forEachTranslatedPair(t *testing.T, enPath, ruPath string) {
	t.Helper()
	en := mustRead(t, enPath)
	ru := mustRead(t, ruPath)

	enSections, ruSections := sectionsOf(string(en)), sectionsOf(string(ru))
	require.Equal(t, len(enSections), len(ruSections),
		"%s and %s have a different number of sections:\nEN: %s\nRU: %s",
		enPath, ruPath, strings.Join(enSections, " | "), strings.Join(ruSections, " | "))

	assert.Equal(t, strings.Count(string(en), "```"), strings.Count(string(ru), "```"),
		"%s and %s: one of them gained or lost a code sample", enPath, ruPath)

	// The links a cold reader arrives by. A broken one in a file linked from an
	// article is the cheapest possible way to lose them.
	for _, doc := range []string{string(en), string(ru)} {
		for _, path := range localLinks(doc) {
			_, err := os.Stat(path)
			assert.NoErrorf(t, err, "%s links to %s, which does not exist", enPath, path)
		}
	}
}

func sectionsOf(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") {
			out = append(out, strings.TrimPrefix(line, "## "))
		}
	}
	return out
}

// localLinks — everything the file points at inside the repository, badges and
// external URLs excluded. Markdown links, and the `src`/`srcset` of the
// <picture> the architecture diagram is shown through: a theme-specific image
// is HTML rather than markdown, and a missing one shows as a broken icon to
// exactly half the readers — the half whose theme nobody tested in.
var linkRe = regexp.MustCompile(`\]\(([^)]+)\)`)
var imgRe = regexp.MustCompile(`(?:src|srcset)="([^"]+)"`)

func localLinks(doc string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{linkRe, imgRe} {
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http") || strings.HasPrefix(target, "#") {
				continue
			}
			out = append(out, strings.TrimSuffix(target, "/"))
		}
	}
	return out
}

// The architecture diagram is four files — two languages times two themes — and
// they are one picture. Hand-editing one of them is how a reader in dark mode
// ends up looking at a version of the engine that no longer exists, silently:
// nothing breaks, the picture just stops being true.
//
// So the pairs are compared by SHAPE. Across themes, everything but the palette
// must be identical; across languages, the number of boxes, lines and captions
// must be — the words differ, the drawing does not.
func TestArchitectureDiagramsStayInStep(t *testing.T) {
	colours := regexp.MustCompile(`(fill|stroke|stop-color)="[^"]*"`)
	for _, pair := range [][2]string{
		{"assets/architecture.light.svg", "assets/architecture.dark.svg"},
		{"assets/architecture.ru.light.svg", "assets/architecture.ru.dark.svg"},
	} {
		light := colours.ReplaceAllString(string(mustRead(t, pair[0])), `$1="…"`)
		dark := colours.ReplaceAllString(string(mustRead(t, pair[1])), `$1="…"`)
		assert.Equalf(t, light, dark,
			"%s and %s differ in more than their colours — one theme is drawing something the other does not",
			pair[0], pair[1])
	}

	for _, theme := range []string{"light", "dark"} {
		en := string(mustRead(t, "assets/architecture."+theme+".svg"))
		ru := string(mustRead(t, "assets/architecture.ru."+theme+".svg"))
		for _, tag := range []string{"<rect", "<text", "<path"} {
			assert.Equalf(t, strings.Count(en, tag), strings.Count(ru, tag),
				"the %s diagrams disagree on how many %s elements the picture has", theme, tag)
		}
	}
}
