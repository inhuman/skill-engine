package skillengine

// Подстановка {{var}} и нормализация значений шага.

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// varRe ловит {{var}} и {{var.field}} — один уровень вложенности.
//
// Глубже намеренно нет: наблюдаемые случаи плоские, а вложенность тянет за
// собой индексы, фильтры и прочий шаблонизатор, которого формат избегает.
// assetRe ловит {{asset:имя}} — подстановку содержимого нагрузки в ТЕКСТ
// инструкции.
//
// Отдельное пространство имён, а не общее с переменными: коллизия имён иначе
// молча выигрывает у одного из двух, и автор не увидит, что именно подставилось.
//
// Так используют kind: text и data — справочник соответствий и шаблон ответа
// бесполезны, если модель их не прочитает. Код и конфиг наоборот идут в
// аргумент инструмента мимо модели.
var assetRe = regexp.MustCompile(`\{\{\s*asset:([a-zA-Z_][a-zA-Z0-9_-]*)\s*\}\}`)

var varRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?)\s*\}\}`)

// expand подставляет {{var}}. Неизвестная переменная превращается в пустую
// строку, а не остаётся текстом: маркер, доехавший до модели, читается ею как
// часть инструкции и порождает вопросы про «переменную var».
func (s *state) expand(text string) string {
	text = assetRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.asset(assetRe.FindStringSubmatch(m)[1])
	})
	return varRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.lookup(varRe.FindStringSubmatch(m)[1])
	})
}

// expandForArgs — подстановка в АРГУМЕНТЫ вызова, а не в инструкцию.
//
// Разница не косметическая. В переменной лежит то, что хост показал бы МОДЕЛИ:
// крупное значение обрезано до превью, и к любому дописан хендл рабочей памяти
// («[mem:id]»). Модели это в помощь — она видит, что данные есть и как их
// дочитать. Скрипту на том конце вызова — мусор: он получает JSON с хвостом и
// падает на разборе.
//
// Живой случай: шаг рендера получал в stdin результат шага
// merge_findings вместе с хвостом «[mem:…]» и отвечал «RENDER_ERROR: stdin не
// парсится как поток JSON»; ход умирал на гейте публикации, за два шага от
// причины. Третье проявление одного класса — после доступа к полю и for_each.
//
// Поэтому в аргументы значение едет ЦЕЛИКОМ и БЕЗ пометки хоста.
func (s *state) expandForArgs(text string) string {
	text = assetRe.ReplaceAllStringFunc(text, func(m string) string {
		return s.asset(assetRe.FindStringSubmatch(m)[1])
	})
	return varRe.ReplaceAllStringFunc(text, func(m string) string {
		return trimHostNote(s.fullValue(s.lookup(varRe.FindStringSubmatch(m)[1])))
	})
}

// asset достаёт содержимое нагрузки, добывая её при первом обращении.
//
// Недоступный ассет разворачивается в ПУСТУЮ строку, как и неизвестная
// переменная: маркер, доехавший до модели, читался бы ею как часть инструкции.
// Отказ при этом не молчит — резолвер сообщает о нём вызывающему.
func (s *state) asset(name string) string {
	if v, ok := s.assetCache[name]; ok {
		return v
	}
	a, ok := s.assets[name]
	if !ok || s.assetsRes == nil {
		return ""
	}
	v, err := s.assetsRes.Resolve(s.assetCtx, name, a)
	if err != nil {
		s.assetCache[name] = ""
		return ""
	}
	s.assetCache[name] = v
	return v
}

// lookup достаёт значение переменной или её поля.
//
// Поле ищется в JSON-объекте, который шаг положил в переменную (структурный
// ответ). Отсутствующее — пустая строка, как и отсутствующая переменная:
// маркер, доехавший до модели, читался бы ею как часть инструкции.
func (s *state) lookup(name string) string {
	if v, ok := s.vars[name]; ok {
		return v
	}
	base, field, hasField := strings.Cut(name, ".")
	if !hasField {
		return ""
	}
	raw, ok := s.vars[base]
	if !ok {
		return ""
	}
	obj, ok := s.objectOf(raw)
	if !ok {
		return ""
	}
	v, ok := obj[field]
	if !ok {
		return ""
	}
	if str, isStr := v.(string); isStr {
		return str
	}
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

// objectOf разбирает значение переменной как JSON-объект.
//
// Значение переменной — это то, что хост показал БЫ модели, а не голый ответ
// инструмента: к нему всегда дописан хендл рабочей памяти («[mem:id]»), а
// крупный вдобавок обрезан до превью. И то и другое ломает разбор, поэтому
// поле молча пустело у ЛЮБОГО результата `call:` — подстановка `{{var.field}}`
// на таких переменных не работала никогда.
//
// Порядок: снять пометку хоста и попробовать; не вышло (обрезано) — взять
// целое из рабочей памяти по хендлу, он для того и дописан.
func (s *state) objectOf(raw string) (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimHostNote(raw)), &obj); err == nil {
		return obj, true
	}
	id := memHandle(raw)
	if id == "" || s.memory == nil {
		return nil, false
	}
	full, ok := s.memory.Get(id)
	if !ok {
		return nil, false
	}
	if err := json.Unmarshal([]byte(full), &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// fullValue возвращает значение переменной ЦЕЛИКОМ.
//
// В переменной лежит то, что хост показал бы модели: крупный результат обрезан
// до превью, а целое — в рабочей памяти под дописанным хендлом. Для промпта это
// правильно, а для КОЛЛЕКЦИИ губительно: цикл по обрезанному списку молча
// обработает часть и отдаст ответ, который выглядит полным (ровно то, ради чего
// в for_each есть потолок с громким сообщением).
func (s *state) fullValue(raw string) string {
	id := memHandle(raw)
	if id == "" || s.memory == nil {
		return raw
	}
	full, ok := s.memory.Get(id)
	if !ok {
		return raw
	}
	return full
}

// trimHostNote срезает пометку, дописанную хостом к результату инструмента:
// «[mem:id]» у целого и «…[mem:id — это ПРЕВЬЮ…]»/«…[обрезано: …]» у крупного.
// Пометка всегда идёт последней строкой — её и режем, не трогая содержимое.
func trimHostNote(s string) string {
	i := strings.LastIndex(s, "\n[")
	if i < 0 {
		return s
	}
	switch tail := s[i+2:]; {
	case strings.HasPrefix(tail, "mem:"), strings.HasPrefix(tail, "обрезано:"):
		return strings.TrimSuffix(strings.TrimSpace(s[:i]), "…")
	}
	return s
}

// expandArgs подставляет {{var}} в СТРОКОВЫЕ значения аргументов, обходя
// вложенные структуры. Числа, флаги и форма объекта остаются как записаны:
// подстановка — про значения, а не про схему вызова.
func (s *state) expandArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = s.expandAny(v)
	}
	return out
}

// callArgs готовит аргументы шага `call:`: подстановка значений плюс перенос
// маршрута доставки, объявленного ассетом.
//
// Ассет объявляет `deliver`, а мост читает маршрут из `_deliver` в аргументах —
// связать их было некому, и объявление оставалось украшением. Живой отказ
// скилл объявлял `deliver: reply` у ассета-рендера, вывод
// шага не помечался как ответ хода, и гейт публикации в MR отвергал ход целиком
// («финальный шаг скилла не зафиксировал результат»). Ровно тот случай, когда
// поле объявлено и валидируется, но живого эффекта не имеет.
//
// Явный `_deliver` в аргументах шага сильнее: он написан для конкретного вызова,
// а объявление ассета — умолчание для всех его потребителей.
func (s *state) callArgs(args map[string]any) map[string]any {
	out := s.expandArgs(args)
	if _, explicit := out["_deliver"]; explicit {
		return out
	}
	to := s.assetDeliver(args)
	if to == "" {
		return out
	}
	if out == nil {
		out = make(map[string]any, 1)
	}
	out["_deliver"] = map[string]any{"to": to}
	return out
}

// assetDeliver возвращает маршрут доставки, объявленный первым ассетом, который
// уходит в аргументы по ссылке.
//
// Значение передаётся КАК ЕСТЬ: имена маршрутов — словарь приложения, и движку
// незачем их толковать. Пусто или "none" — доставки нет: это единственное
// значение, которое движок понимает сам, потому что означает отказ от маршрута,
// а не маршрут.
func (s *state) assetDeliver(args map[string]any) string {
	for _, name := range AssetRefsInArgs(args) {
		a, ok := s.assets[name]
		if !ok {
			continue
		}
		if route := strings.TrimSpace(a.Deliver); route != "" && route != "none" {
			return route
		}
	}
	return ""
}

func (s *state) expandAny(v any) any {
	switch t := v.(type) {
	case string:
		return s.expandForArgs(t)
	case map[string]any:
		// {from: "asset:имя"} — ссылка на нагрузку. Содержимое подставляется
		// ЗДЕСЬ, хост-сайд: шаг `call:` формируется описанием, а не моделью,
		// поэтому нагрузка до модели не доходит вовсе — ровно то, ради чего
		// ассеты и существуют.
		if from, ok := t["from"].(string); ok && len(t) == 1 {
			if name, isAsset := strings.CutPrefix(from, "asset:"); isAsset {
				return s.asset(name)
			}
		}
		return s.expandArgs(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = s.expandAny(e)
		}
		return out
	default:
		return v
	}
}

// normalizeOneOf сводит вольный ответ шага к одному из допустимых значений.
//
// Ищется ПОСЛЕДНЕЕ вхождение: модель часто перечисляет варианты, прежде чем
// назвать выбранный («определить t1 или foreign… Результат: foreign»), и первое
// совпадение — это перечисление, а не решение. Тот же приём, что и при снятии
// вердикта из рассуждения.
func normalizeOneOf(text string, allowed []string) string {
	if len(allowed) == 0 {
		return text
	}
	trimmed := strings.Trim(strings.TrimSpace(text), "\"'`")

	// 1. Ответ — ровно одно из значений. Так отвечает модель с грамматикой, и
	//    так же чаще всего отвечает послушная модель без неё.
	for _, want := range allowed {
		if strings.EqualFold(trimmed, want) {
			return want
		}
	}

	// 2. Значение после маркера решения. Модель, которой велели ответить одним
	//    словом, всё равно пишет «Итог: … Результат: foreign» — берём то, что
	//    стоит ПОСЛЕ маркера, а не последнее в тексте.
	if v, ok := afterDecisionMarker(trimmed, allowed); ok {
		return v
	}

	// 3. Значение встретилось ровно одно (пусть и несколько раз) — берём его.
	if v, ok := onlyMentioned(trimmed, allowed); ok {
		return v
	}

	// 4. Одно значение названо СТРОГО ЧАЩЕ другого: назвав выбор, модель обычно
	//    его повторяет («нужно определить t1 или foreign; здесь foreign»).
	//    Ровное число упоминаний решением не считается — именно на нём ломалась
	//    прежняя версия.
	if v, ok := mostMentioned(trimmed, allowed); ok {
		return v
	}

	// 5. Иначе — НИЧЕГО. Прежняя версия брала последнее вхождение и ошибалась
	//    на «Это точно t1, а не foreign»: отрицание в конце — обычная русская
	//    конструкция, и догадка давала прямо противоположный ответ. Пустое
	//    значение уводит switch в default, где скилл может честно переспросить,
	//    а неверная догадка молча ведёт ход не туда.
	return ""
}

// decisionMarkers — слова, после которых модель называет выбор.
var decisionMarkers = []string{"результат:", "итог:", "ответ:", "вывод:", "выбор:", "решение:"}

func afterDecisionMarker(text string, allowed []string) (string, bool) {
	low := strings.ToLower(text)
	at := -1
	for _, m := range decisionMarkers {
		if i := strings.LastIndex(low, m); i > at {
			at = i + len(m)
		}
	}
	if at < 0 {
		return "", false
	}
	tail := low[at:]
	best, pos := "", -1
	for _, want := range allowed {
		if i := strings.Index(tail, strings.ToLower(want)); i >= 0 && (pos < 0 || i < pos) {
			best, pos = want, i
		}
	}
	return best, best != ""
}

// mostMentioned возвращает значение, упомянутое строго чаще остальных.
func mostMentioned(text string, allowed []string) (string, bool) {
	low := strings.ToLower(text)
	best, bestN, tie := "", 0, false
	for _, want := range allowed {
		n := countWholeWord(low, strings.ToLower(want))
		switch {
		case n > bestN:
			best, bestN, tie = want, n, false
		case n == bestN && n > 0:
			tie = true
		}
	}
	if bestN == 0 || tie {
		return "", false
	}
	return best, true
}

func onlyMentioned(text string, allowed []string) (string, bool) {
	low := strings.ToLower(text)
	var found string
	for _, want := range allowed {
		if countWholeWord(low, strings.ToLower(want)) > 0 {
			if found != "" && !strings.EqualFold(found, want) {
				return "", false // упомянуто несколько — выбирать нельзя
			}
			found = want
		}
	}
	return found, found != ""
}

// countWholeWord считает вхождения needle как ОТДЕЛЬНОГО слова.
//
// Простой Count врёт, когда одно допустимое значение содержится в другом:
// в наборе [found, not_found] ответ «not_found» засчитывался обоим, оба слоя
// видели ничью и возвращали пустоту — шаг молча оставался без значения, switch
// уходил в default, и ход отвечал служебной переменной. Границей считается всё, кроме букв, цифр и подчёркивания:
// подчёркивание — часть имён вроде not_found.
func countWholeWord(text, needle string) int {
	if needle == "" {
		return 0
	}
	n, from := 0, 0
	for {
		i := strings.Index(text[from:], needle)
		if i < 0 {
			return n
		}
		i += from
		if isWordBoundary(text, i-1) && isWordBoundary(text, i+len(needle)) {
			n++
		}
		from = i + len(needle)
	}
}

// isWordBoundary сообщает, что позиция вне слова: за пределами строки либо
// не буква/цифра/подчёркивание.
func isWordBoundary(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return true
	}
	r := rune(text[i])
	return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// MemSuffix — суффикс переменной с хендлом рабочей памяти: результат шага
// `save_as: pods` кладёт хендл в `pods.mem`.
const MemSuffix = ".mem"

// memRefRe ловит хендл рабочей памяти в тексте результата. Формат «[mem:id]» —
// общая конвенция хоста и движка: так хендл видит и модель в превью, и описание
// скилла в подстановке. Разбором текста, а не расширением интерфейса, потому что
// хендл и предназначен для того, чтобы его читали из текста.
var memRefRe = regexp.MustCompile(`\[mem:([a-zA-Z0-9_-]+)`)

// memHandle возвращает хендл рабочей памяти из результата инструмента, если он
// там есть.
func memHandle(text string) string {
	if m := memRefRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// SkippedSuffix — суффикс переменной со списком пропущенных веток развилки:
// `collect: findings` кладёт их в `findings.skipped`.
const SkippedSuffix = ".skipped"
