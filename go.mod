module github.com/inhuman/skill-engine

go 1.26.0

// Боевой код зависимостей НЕ имеет — только stdlib (см. imports_test.go).
// Ниже — то, чем пользуются тесты; в сборку встраивающего приложения это не едет.
require (
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
)
