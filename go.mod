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
