package skillengine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Ассеты — именованные нагрузки, которые скилл передаёт инструменту ПО ССЫЛКЕ,
// не пропуская через контекст модели.
//
// Зачем: модель не может точно воспроизвести многокилобайтный литерал через
// аргумент инструмента — она его РЕГЕНЕРИРУЕТ и портит (живой отказ 077: шесть
// SyntaxError подряд на скрипте рендера, потом захардкоженная сдача).
//
// Описываются ДВУМЯ независимыми осями. Их намеренно не сводят в один
// перечень: иначе получится комбинаторика (code_inline, code_repo, config_mcp…),
// и каждый новый источник потребует столько значений, сколько есть видов.

// AssetKind — ЧТО это по существу.
//
// Влияет на то, куда нагрузка попадает: code и config идут в аргумент
// инструмента мимо модели, text и data подставляются в инструкцию — справочник
// соответствий бесполезен, если модель его не прочитает.
type AssetKind string

const (
	AssetCode   AssetKind = "code"
	AssetText   AssetKind = "text"
	AssetConfig AssetKind = "config"
	AssetData   AssetKind = "data"
)

// AssetSource — ОТКУДА берётся содержимое.
// Маршруты доставки вывода инструмента, потребившего ассет. Перечислены здесь,
// а не строками по месту: значения читает и валидация, и перенос в `_deliver`.
const (
	AssetDeliverReply = "reply"
	AssetDeliverFile  = "file"
	AssetDeliverNone  = "none"
)

type AssetSource string

const (
	AssetInline   AssetSource = "inline"
	AssetRepo     AssetSource = "repo"
	AssetUserFile AssetSource = "user_file"
	// AssetMCP — результат MCP-вызова. ЭТО путь для внешних данных: вики-страница,
	// список из внешней системы.
	//
	// Прямого HTTP (source: url) намеренно нет: интеграции с внешними системами
	// идут только через MCP-мультиплексор, и этот путь бесплатно даёт политику
	// доступа, аудит вызова и pattern-правила.
	AssetMCP AssetSource = "mcp"
)

// Asset — объявление нагрузки в описании скилла.
type Asset struct {
	Kind   AssetKind   `yaml:"kind,omitempty"`
	Source AssetSource `yaml:"source,omitempty"`
	// Content — содержимое для source: inline.
	Content string `yaml:"content,omitempty"`
	// Ref — адрес для внешних источников: «проект@ветка:путь» | «путь в
	// user_files» | «сервер:инструмент».
	Ref string `yaml:"ref,omitempty"`
	// Args — аргументы MCP-вызова для source: mcp. Поддерживают {{var}}.
	Args map[string]any `yaml:"args,omitempty"`
	// Lang — язык для kind: code (python|sql|bash…). Даёт линтеру повод
	// проверить синтаксис ДО того, как скилл исполнится в проде.
	Lang string `yaml:"lang,omitempty"`
	// Deliver — куда девать ВЫВОД инструмента, потребившего ассет:
	// reply — вывод становится ответом хода; file — доставляется файлом;
	// пусто — возвращается шагу.
	//
	// Существует потому, что модель забывает прикрепить хрупкий аргумент
	// доставки (живой отказ 077: рендер отработал, а файл пользователю не
	// уехал). Автор скилла объявляет маршрут, мост фиксирует его сам.
	Deliver string `yaml:"deliver,omitempty"`
	// Description — для чего ассет; читает ЧЕЛОВЕК, правящий скилл. Особенно
	// нужно внешним: содержимое не видно в файле.
	Description string `yaml:"description,omitempty"`
	// Fetch — политика получения для внешних источников.
	Fetch *Fetch `yaml:"fetch,omitempty"`
}

// Fetch — как тянуть внешний ассет.
//
// Внешний ассет тянется В ХОДЕ, а не из периодического кеша: смысл внешнего
// источника — свежесть. Вики-страница правится, список сервисов меняется;
// отдать вчерашнюю копию значит отменить причину, по которой ассет сделали
// внешним. Цена — сеть в горячем пути, и лечится она этой политикой, а не
// запретом.
type Fetch struct {
	// TTL — сколько содержимое считается свежим. Пусто/0 — тянуть каждый раз.
	TTL time.Duration `yaml:"ttl,omitempty"`
	// Timeout — потолок на ОДНУ попытку. Ход не должен висеть на чужой
	// недоступности.
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// Retries — повторы сверх первой попытки, с экспоненциальной паузой.
	// Повторяется только transient: повтор на 404 исход не изменит.
	Retries int `yaml:"retries,omitempty"`
	// OnUnavailable — что делать, когда достать не удалось:
	// fail (умолчание) | stale — прошлая копия | empty — пустота.
	OnUnavailable string `yaml:"on_unavailable,omitempty"`
}

// AssetResolver достаёт содержимое ассета. Домен (репозитории, user_files,
// MCP) живёт за интерфейсом — движок про них не знает.
type AssetResolver interface {
	Resolve(ctx context.Context, name string, a Asset) (string, error)
}

// validateAssets проверяет объявления до исполнения: связки полей и то, что
// ссылка ведёт хоть куда-то.
func validateAssets(assets map[string]Asset) error {
	for name, a := range assets {
		src := a.Source
		if src == "" {
			src = AssetInline
		}
		switch src {
		case AssetInline:
			if strings.TrimSpace(a.Content) == "" {
				return fmt.Errorf("ассет %q: inline без content", name)
			}
		case AssetRepo, AssetUserFile, AssetMCP:
			if strings.TrimSpace(a.Ref) == "" {
				return fmt.Errorf("ассет %q: источник %q требует ref", name, src)
			}
		default:
			return fmt.Errorf("ассет %q: неизвестный источник %q", name, src)
		}
		if a.Kind == AssetCode && strings.TrimSpace(a.Lang) == "" {
			return fmt.Errorf("ассет %q: code без lang — линтеру нечем проверить синтаксис", name)
		}
		switch a.Deliver {
		case "", AssetDeliverReply, AssetDeliverFile, AssetDeliverNone:
		default:
			return fmt.Errorf("ассет %q: неизвестный deliver %q", name, a.Deliver)
		}
		if a.Fetch != nil {
			switch a.Fetch.OnUnavailable {
			case "", "fail", "stale", "empty":
			default:
				return fmt.Errorf("ассет %q: неизвестный on_unavailable %q", name, a.Fetch.OnUnavailable)
			}
		}
	}
	return nil
}

// AssetRefsInText перечисляет ассеты, подставляемые в текст ({{asset:имя}}).
func AssetRefsInText(text string) []string {
	matches := assetRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// AssetRefsInArgs перечисляет ассеты, передаваемые по ссылке ({from: "asset:имя"}) —
// то есть мимо контекста модели. Разница с AssetRefsInText не стилистическая:
// от неё зависит, увидит ли модель содержимое.
func AssetRefsInArgs(args map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if from, ok := t["from"].(string); ok && len(t) == 1 {
				if name, isAsset := strings.CutPrefix(from, "asset:"); isAsset {
					out = append(out, name)
					return
				}
			}
			for _, nested := range t {
				walk(nested)
			}
		case []any:
			for _, nested := range t {
				walk(nested)
			}
		}
	}
	walk(args)
	return out
}
