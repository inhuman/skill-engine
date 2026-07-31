package skillengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Нормализация значения шага-классификатора на ЖИВЫХ формулировках.
//
// Прежняя версия брала последнее вхождение и ошибалась на «Это точно t1, а не
// foreign» — отрицание в конце фразы обычно для русского, и догадка давала
// прямо противоположный ответ. Теперь при неоднозначности возвращается пусто:
// switch уходит в default, где скилл может переспросить, вместо того чтобы
// молча пойти не туда.
func TestNormalizeOneOfOnLiveWordings(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, c := range []struct{ text, want, why string }{
		{"foreign", "foreign", "чистый ответ"},
		{"  t1\n", "t1", "с пробелами"},
		{"Итог: определил по префиксу. Результат: foreign", "foreign", "значение после маркера"},
		{"Ответ: t1", "t1", "маркер «ответ»"},
		{"Не foreign, а t1", "", "оба упомянуты, маркера нет → неоднозначно"},
		{"Это точно t1, а не foreign", "", "ЛОВУШКА: последнее вхождение дало бы foreign"},
		{"Выбираю между t1 и foreign", "", "перечисление без решения"},
		{"это внутренний ресурс t1, сверяться с репой", "t1", "упомянуто одно значение"},
		{"ничего не понял", "", "значения нет вовсе"},
	} {
		t.Run(c.why, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeOneOf(c.text, allowed), c.text)
		})
	}
}

// «Не foreign, а t1» — неоднозначный случай: оба значения названы, маркера нет.
// Пустой результат честнее догадки.
func TestNormalizeOneOfAmbiguousReturnsEmpty(t *testing.T) {
	assert.Empty(t, normalizeOneOf("Не foreign, а t1", []string{"t1", "foreign"}))
}

func TestNormalizeOneOfNoConstraint(t *testing.T) {
	assert.Equal(t, "любой текст", normalizeOneOf("любой текст", nil))
}

// Одно допустимое значение — часть другого (found ⊂ not_found). Простой подсчёт
// вхождений засчитывал ответ обоим, оба слоя видели ничью и возвращали пустоту:
// шаг молча оставался без значения. Живой случай: — поисковый скилл.
func TestNormalizeOneOf_OverlappingValues(t *testing.T) {
	allowed := []string{"found", "not_found"}
	for _, c := range []struct{ in, want string }{
		{"not_found", "not_found"},
		{`"not_found"`, "not_found"},
		{"found", "found"},
		{"Ответ: not_found", "not_found"},
		{"нет подходящих страниц — not_found", "not_found"},
	} {
		if got := normalizeOneOf(c.in, allowed); got != c.want {
			t.Errorf("normalizeOneOf(%q) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

// Кавычки вокруг ответа: модель без грамматики отвечает JSON-строкой.
func TestNormalizeOneOf_Quoted(t *testing.T) {
	allowed := []string{"t1", "foreign"}
	for _, in := range []string{`"foreign"`, "'foreign'", "`foreign`", ` "foreign" `} {
		if got := normalizeOneOf(in, allowed); got != "foreign" {
			t.Errorf("normalizeOneOf(%q) = %q", in, got)
		}
	}
}
