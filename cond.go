package skillengine

// Условия ветвления: `var == значение`, `var != значение`,
// `var is [not] empty`.

import (
	"fmt"
	"regexp"
	"strings"
)

var condRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*(==|!=)\s*(.*?)\s*$`)

// emptyCondRe — условие «шаг не дал результата»: `var is empty` / `var is not empty`.
//
// Зачем отдельная форма, а не сравнение с "". Шаг, отработавший неудачно, кладёт
// в переменную не пустоту, а помеченный отказ (ERROR:/DENIED:) — различать их
// нужно ПОЛИТИКЕ, но ветвлению «не нашли — ищем иначе» они оба означают одно.
// Этот паттерн встретился трижды в одном живом описании, а повторение в трёх
// местах — признак, что смысл принадлежит движку, а не описанию (то же
// рассуждение, что и у ErrorPolicy).
var emptyCondRe = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s+is\s+(not\s+)?empty\s*$`)

// isBlank — «шаг не дал полезного результата»: пусто или отказ.
func isBlank(v string) bool {
	t := strings.TrimSpace(v)
	return t == "" || strings.HasPrefix(t, "ERROR:") || strings.HasPrefix(t, "DENIED:")
}

// parseCond разбирает условие вида `var == value` / `var != value`.
// Значение может быть пустым (`var == `) — так проверяется незаполненность.
func parseCond(cond string) (name, op, want string, err error) {
	if m := emptyCondRe.FindStringSubmatch(cond); m != nil {
		op = "is empty"
		if strings.TrimSpace(m[2]) != "" {
			op = "is not empty"
		}
		return m[1], op, "", nil
	}
	m := condRe.FindStringSubmatch(cond)
	if m == nil {
		return "", "", "", fmt.Errorf("условие %q: ожидается «var == значение», «var != значение» или «var is [not] empty»", cond)
	}
	return m[1], m[2], strings.Trim(m[3], `"'`), nil
}

func (s *state) eval(cond string) (bool, error) {
	name, op, want, err := parseCond(cond)
	if err != nil {
		return false, err
	}
	switch op {
	case "is empty":
		return isBlank(s.lookup(name)), nil
	case "is not empty":
		return !isBlank(s.lookup(name)), nil
	}
	got := strings.TrimSpace(s.lookup(name))
	if op == "==" {
		return got == want, nil
	}
	return got != want, nil
}
