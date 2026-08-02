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

// Withdrawn: both carried names belonging to the private installation the
// format grew in. The history was rewritten and the tags removed, but the
// public module proxy caches a version's content immutably — so these two stay
// fetchable through it whatever happens to the repository. Retracting is what
// tells the tooling not to select them and warns whoever already has one.
//
// They are retracted rather than re-pointed at the cleaned commits on purpose:
// a version whose content changes breaks go.sum for everyone who fetched it.
retract (
	v0.1.0
	v0.0.1
)
