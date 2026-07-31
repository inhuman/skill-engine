package skillengine

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// Unmarshal — разбор YAML в структуру. Движок НЕ тянет yaml-библиотеку сам:
// описание скиллов всё равно читает встраивающее приложение, у него уже есть свой разбор, и
// вторая копия здесь стала бы его зависимостью — с его версиями и его
// конфликтами. Нужен ровно один метод, поэтому это функция, а не интерфейс.
//
// Подходит `yaml.Unmarshal` из gopkg.in/yaml.v3 без обёртки.
type Unmarshal func(data []byte, v any) error

// SchemaYAML — контракт формата скилла (JSON Schema, записанная в YAML).
//
// Живёт рядом с движком, а не только в спеке: по ней валидируется скилл при
// записи, её же отдают модели, когда та пишет скилл по просьбе человека.
// Спека (specs/skill-programs/contracts/) — документация; источник истины
// для кода здесь.
//
//go:embed skill.schema.yaml
var SchemaYAML string

// SchemaSummary — компактная справка по формату: поля и первая строка описания
// каждого.
//
// Полная схема — 56 КБ. Отдать их модели значит съесть контекст ради
// справочника: тот же класс удушения, от которого формат защищает усечением
// результатов инструментов. Автору скилла нужен перечень полей и смысл каждого;
// подробности — в спеке, для человека.
func SchemaSummary(unmarshal Unmarshal) string {
	if unmarshal == nil {
		return ""
	}
	var root struct {
		Properties map[string]struct {
			Description string   `yaml:"description"`
			Type        string   `yaml:"type"`
			Enum        []string `yaml:"enum"`
			Ref         string   `yaml:"$ref"`
		} `yaml:"properties"`
		Required []string `yaml:"required"`
		Defs     map[string]struct {
			Description string `yaml:"description"`
			Properties  map[string]struct {
				Description string   `yaml:"description"`
				Type        string   `yaml:"type"`
				Enum        []string `yaml:"enum"`
			} `yaml:"properties"`
		} `yaml:"$defs"`
	}
	if err := unmarshal([]byte(SchemaYAML), &root); err != nil {
		return "" // схема битая — пусть вызывающий отдаст полную
	}

	req := map[string]bool{}
	for _, r := range root.Required {
		req[r] = true
	}

	var b strings.Builder
	b.WriteString("ФОРМАТ СКИЛЛА (обязательные помечены *)\n\n")
	for _, name := range sortedKeys(root.Properties) {
		p := root.Properties[name]
		mark := " "
		if req[name] {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %-22s %s\n", mark, name, firstLine(p.Description))
	}

	b.WriteString("\nШАГ (ровно одно действие: instruction | call | set | switch | if | exit | delegate | for_each | parallel)\n\n")
	if step, ok := root.Defs["Step"]; ok {
		for _, name := range sortedKeys(step.Properties) {
			f := step.Properties[name]
			desc := firstLine(f.Description)
			if desc == "" {
				// Поле-ссылка: описание лежит у типа, на который оно ссылается.
				if d, ok := root.Defs[capitalizeRef(name)]; ok {
					desc = firstLine(d.Description)
				}
			}
			fmt.Fprintf(&b, "  %-18s %s\n", name, desc)
		}
	}

	b.WriteString("\nОСТАЛЬНЫЕ КОНСТРУКЦИИ\n\n")
	for _, name := range sortedKeys(root.Defs) {
		if name == "Step" {
			continue
		}
		fmt.Fprintf(&b, "  %-18s %s\n", name, firstLine(root.Defs[name].Description))
	}
	return b.String()
}

// capitalizeRef переводит имя поля в имя типа: for_each → ForEach, call → Call.
func capitalizeRef(field string) string {
	parts := strings.Split(field, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	// «[есть] …», «[спека] …» — пометки статуса, автору скилла они не нужны.
	if i := strings.Index(line, "] "); i > 0 && strings.HasPrefix(line, "[") {
		line = line[i+2:]
	}
	return line
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
