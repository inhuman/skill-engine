package skillengine

import (
	"fmt"
	"strconv"
	"strings"
)

// Версии формата и скилла (semver).
//
// Две РАЗНЫЕ вещи, и путать их дорого:
//
//	EngineVersion       — версия ФОРМАТА, которую понимает этот движок;
//	Skill.EngineVersion — версия, под которую написан скилл, то есть
//	                      требуемый им МИНИМУМ движка.
//
// Зачем вообще. Скиллы пользователей живут в user_files и НЕ обновляются с
// деплоем: сменив семантику поля, мы молча меняем поведение чужих скиллов,
// написанных по старым правилам. Живой риск — `tools: []`: сегодня это
// «инструментов не выдавать», центральная конструкция формата; начни пустой
// список значить «набор потока», и старые скиллы получат доступ, который автор
// снимал намеренно.

// EngineVersion — версия формата, поддерживаемая этим движком.
//
//	major — несовместимое изменение (скилл прошлого мажора без миграции не читается);
//	minor — добавлено необязательное поле (скилл, который им пользуется, требует
//	        движок не ниже этой минорной);
//	patch — исправления движка, формат не менялся.
const EngineVersion = "1.0.0"

// LegacyEngineVersion — что считать объявленной версией, когда поле не задано
// (скиллы, написанные до её введения).
const LegacyEngineVersion = "1.0.0"

// CheckEngineVersion сообщает, может ли движок исполнить скилл.
//
// Отказ ВНЯТНЫЙ, а не молчаливое исполнение по старым правилам: скилл, который
// требует поля из будущей версии, при тихом фолбэке отработает «как-то» — то
// есть даст правдоподобный неверный результат вместо честной ошибки. Это тот же
// класс отказа, на котором обжигались с harmony-плагином, где выброшенное поле
// не отвергалось, а исчезало.
func CheckEngineVersion(declared string) error {
	if declared == "" {
		declared = LegacyEngineVersion
	}
	want, err := parseVersion(declared)
	if err != nil {
		return fmt.Errorf("skill_engine_version %q: не semver", declared)
	}
	have, _ := parseVersion(EngineVersion)
	if want.compare(have) > 0 {
		return fmt.Errorf("скилл требует версию формата %s, движок умеет %s", declared, EngineVersion)
	}
	return nil
}

// CompareSkillVersions сравнивает версии ОДНОГО скилла: >0 когда a новее b.
//
// Нужно там, где сегодня сравнивается хеш содержимого: хеш отвечает на вопрос
// «изменилось ли», но не на «что новее», поэтому по нему нельзя ни откатиться
// осознанно, ни разрешить расхождение.
//
// Пустая версия считается самой старой: скилл, её не объявивший, не должен
// перезаписывать тот, который объявил.
func CompareSkillVersions(a, b string) (int, error) {
	va, err := parseSkillVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseSkillVersion(b)
	if err != nil {
		return 0, err
	}
	return va.compare(vb), nil
}

func parseSkillVersion(v string) (version, error) {
	if v == "" {
		return version{}, nil
	}
	p, err := parseVersion(v)
	if err != nil {
		return version{}, fmt.Errorf("skill_version %q: не semver", v)
	}
	return p, nil
}

// version — major.minor.patch. Своя реализация вместо semver-библиотеки: движок
// встраивают в чужое приложение, и каждая зависимость здесь становится
// зависимостью встраивающего приложения — с его версиями и его конфликтами. Из semver нужны ровно
// разбор и сравнение трёх чисел, суффиксы предрелизов формату не нужны.
type version struct{ major, minor, patch int }

func parseVersion(s string) (version, error) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 4)
	if len(parts) != 3 {
		return version{}, fmt.Errorf("нужны три части major.minor.patch")
	}
	var v version
	for i, dst := range []*int{&v.major, &v.minor, &v.patch} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return version{}, fmt.Errorf("часть %q не число", parts[i])
		}
		*dst = n
	}
	return v, nil
}

// compare: >0 когда получатель новее аргумента.
func (v version) compare(o version) int {
	for _, p := range [][2]int{{v.major, o.major}, {v.minor, o.minor}, {v.patch, o.patch}} {
		if p[0] != p[1] {
			if p[0] > p[1] {
				return 1
			}
			return -1
		}
	}
	return 0
}
