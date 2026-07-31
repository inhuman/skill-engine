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
// аргумент инструмента — она его РЕГЕНЕРИРУЕТ и портит (живой отказ: шесть
// SyntaxError подряд на скрипте рендера, потом захардкоженная сдача).
//
// Описываются ДВУМЯ независимыми осями. Их намеренно не сводят в один
// перечень: иначе получится комбинаторика (code_inline, code_repo, config_mcp…),
// и каждый новый источник потребует столько значений, сколько есть видов.

// Род ассета, источник и маршрут доставки — СЛОВАРЬ ПРИЛОЖЕНИЯ, не движка.
//
// Движок не знает, какие бывают роды нагрузки, откуда её берут и куда девают
// вывод. Знает он одно: содержимое либо лежит здесь, либо доступно по адресу.
// Всё остальное разбирает резолвер встраивающего приложения — поэтому в
// объявлении просто строки, а не перечисления.

// Asset — объявление нагрузки в описании скилла.
type Asset struct {
	Kind   string `yaml:"kind,omitempty"`
	Source string `yaml:"source,omitempty"`
	// Content — содержимое для source: inline.
	Content string `yaml:"content,omitempty"`
	// Ref — адрес для внешних источников: «проект@ветка:путь» | «путь в
	// хранилище файлов» | «сервер:инструмент».
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
	// доставки (живой отказ: рендер отработал, а файл пользователю не
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

// AssetResolver достаёт содержимое ассета. Домен (репозитории, хранилище файлов,
// MCP) живёт за интерфейсом — движок про них не знает.
type AssetResolver interface {
	Resolve(ctx context.Context, name string, a Asset) (string, error)
}

// validateAssets проверяет объявления до исполнения: связки полей и то, что
// ссылка ведёт хоть куда-то.
func validateAssets(assets map[string]Asset) error {
	for name, a := range assets {
		hasContent := strings.TrimSpace(a.Content) != ""
		hasRef := strings.TrimSpace(a.Ref) != ""
		switch {
		case !hasContent && !hasRef:
			return fmt.Errorf("ассет %q: нет ни content, ни ref — нечего исполнять", name)
		case hasContent && hasRef:
			return fmt.Errorf("ассет %q: заданы и content, и ref — непонятно, что брать", name)
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
