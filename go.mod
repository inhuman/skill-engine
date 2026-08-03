module github.com/inhuman/skill-engine

go 1.26.0

// Production code has NO dependencies — stdlib only (see imports_test.go).
// Below is what the tests use; none of it reaches the embedder's build.
require (
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
)

// Withdrawn — every version published before this one.
//
// The first two carried names belonging to the private installation this format
// grew in. The rest carried its VOCABULARY: word lists baked into the engine and
// the linter, and a shipped example built from one application's dictionary. A
// library used by an agent about a kitchen, one about a car fleet and one about
// a warehouse must know none of that, and a version that does is one an
// embedder should not pick up.
//
// The public module proxy caches a version's content immutably, so all of them
// stay fetchable through it whatever happens to this repository. Retracting is
// what tells the tooling not to select them and warns whoever already has one.
//
// They are retracted rather than re-pointed at the cleaned commits on purpose:
// a version whose content changes breaks go.sum for everyone who fetched it.
retract (
	v0.4.0
	v0.3.0
	v0.2.0
	v0.1.0
	v0.0.1
)
