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
	en := mustRead(t, "README.md")
	ru := mustRead(t, "README.ru.md")

	enSections, ruSections := sectionsOf(string(en)), sectionsOf(string(ru))
	require.Equal(t, len(enSections), len(ruSections),
		"the two READMEs have a different number of sections:\nEN: %s\nRU: %s",
		strings.Join(enSections, " | "), strings.Join(ruSections, " | "))

	assert.Equal(t, strings.Count(string(en), "```"), strings.Count(string(ru), "```"),
		"one README gained or lost a code sample")

	// The links a cold reader arrives by. A broken one in a file linked from an
	// article is the cheapest possible way to lose them.
	for _, doc := range []string{string(en), string(ru)} {
		for _, path := range localLinks(doc) {
			_, err := os.Stat(path)
			assert.NoErrorf(t, err, "README links to %s, which does not exist", path)
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

// localLinks — markdown links pointing inside the repository, badges and
// external URLs excluded.
var linkRe = regexp.MustCompile(`\]\(([^)]+)\)`)

func localLinks(doc string) []string {
	var out []string
	for _, m := range linkRe.FindAllStringSubmatch(doc, -1) {
		target := m[1]
		if strings.HasPrefix(target, "http") || strings.HasPrefix(target, "#") {
			continue
		}
		out = append(out, strings.TrimSuffix(target, "/"))
	}
	return out
}
