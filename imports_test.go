package skillengine_test

import (
	"go/build"
	"strings"
	"testing"
)

// allowedTestDeps — зависимости, разрешённые ТОЛЬКО тестам. У боевого кода их
// быть не должно ни одной: движок встраивают в чужое приложение, и каждая
// зависимость здесь становится зависимостью встраивающего — с его версиями и его
// конфликтами. Разбор YAML движок берёт параметром (тип Unmarshal), сравнение
// версий реализовано на месте: из semver-библиотеки нужны были три числа.
var allowedTestDeps = []string{
	"gopkg.in/yaml.v3",
	"github.com/stretchr/testify/assert",
	"github.com/stretchr/testify/require",
}

// Библиотека обязана оставаться самостоятельной: формат скиллов и его исполнение
// не знают про встраивающее приложение. Это не эстетика — на этом держится
// возможность проверять формат без поднятия половины чужой системы (тесты пакета
// не трогают ни MCP, ни базу, ни шлюз инференса).
//
// Связь появляется незаметно: удобный тип из соседнего пакета, «всего одна»
// константа. Поэтому граница проверяется тестом, а не договорённостью.
//
// Проверяются и боевые импорты, и тестовые: тест, потянувший зависимость, делает
// библиотеку тяжелее ровно так же.
func TestEngineStaysSelfContained(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("разбор пакета: %v", err)
	}

	// Боевой код — только stdlib. Ни одного исключения: как только появится
	// первое, появится и второе.
	for _, imp := range pkg.Imports {
		if external(imp) {
			t.Errorf("боевой код импортирует %s — у движка не должно быть зависимостей", imp)
		}
	}
	// Тестам можно из короткого списка: они в чужую сборку не попадают.
	for _, imports := range [][]string{pkg.TestImports, pkg.XTestImports} {
		for _, imp := range imports {
			if external(imp) && !slicesContains(allowedTestDeps, imp) {
				t.Errorf("тест импортирует %s — зависимость вне объявленного списка", imp)
			}
		}
	}
}

// external — импорт не из stdlib (домен в первом сегменте) и не сам движок:
// тесты из внешнего пакета импортируют его по имени, и это не зависимость, а
// способ проверять публичный API снаружи.
func external(imp string) bool {
	if imp == selfModule {
		return false
	}
	head, _, _ := strings.Cut(imp, "/")
	return strings.Contains(head, ".")
}

const selfModule = "github.com/inhuman/skill-engine"

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
