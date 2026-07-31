package skillengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Живой скилл целиком (структура настоящего скилла, имена условные — пакет
// проектируется отчуждаемым). Проверяется, что форма ВЫРАЖАЕТ скилл: четыре
// гварда, которые словами были бы просьбами, здесь свойства шагов.
const exampleFlow = `
tools: ["code_search", "read_file", "list_tree"]
steps:
  - name: classify
    instruction: "Определи владельца ресурса. Ответь одним словом: internal или foreign."
    tools: []
    save_as: owner

  - name: extract
    instruction: "Верни имя ресурса или пустую строку."
    tools: []
    save_as: resource

  - set:
      var: doc_path
      value: "docs/resources/{{resource}}.md"

  - switch:
      var: owner
      cases:
        foreign:
          - name: answer_from_knowledge
            instruction: "Ответь из общих знаний."
            tools: []
            save_as: answer
        internal:
          - name: search_schema
            instruction: "Найди схему {{resource}}. Пусто или ошибка — верни MISS."
            tools: ["code_search"]
            max_calls: 1
            save_as: hit
            on_error: continue
          - if:
              cond: "hit == MISS"
              then:
                - name: read_doc
                  instruction: "Прочитай {{doc_path}}. Не нашёл — верни MISS."
                  tools: ["read_file"]
                  max_calls: 1
                  save_as: hit
                  on_error: continue
          - if:
              cond: "hit == MISS"
              then:
                - name: walk_tree
                  instruction: "Обойди дерево и найди файл схемы."
                  tools: ["list_tree"]
                  max_calls: 8
                  save_as: hit
                  on_error: continue
          - name: answer_from_schema
            instruction: "По найденному ({{hit}}) ответь. Ни одного утверждения без строки из выдачи."
            tools: []
            save_as: answer
      default:
        - name: ask_again
          instruction: "Переспроси имя ресурса."
          tools: []
          save_as: answer
`

func TestExampleFlow_ForeignBranchGetsNoTools(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(exampleFlow), &f))
	require.NoError(t, f.Validate())

	r := &fakeRunner{answer: map[string]string{
		"classify": "foreign", "extract": "aws_instance",
		"answer_from_knowledge": "ответ из общих знаний",
	}}
	vars, _, err := ExecuteWith(context.Background(), &f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "ответ из общих знаний", vars["answer"])

	// Главная проверка: на чужом ресурсе в репозиторий не ходили ФИЗИЧЕСКИ.
	// Иначе это выглядело бы как «в репу за этим НЕ ходи, потратишь вызовы впустую».
	for _, s := range r.seen {
		assert.Empty(t, s.Tools, "шаг %q получил инструменты, хотя ветка их не даёт", s.Name)
	}
	assert.Len(t, r.seen, 3, "классификация, извлечение, ответ — и ни одного похода за данными")
}

func TestExampleFlow_InternalFallbackCascade(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(exampleFlow), &f))

	r := &fakeRunner{answer: map[string]string{
		"classify": "internal", "extract": "vpc_vip",
		"search_schema": "MISS", "read_doc": "MISS",
		"walk_tree":          "нашёл internal/schema.go: l2_enabled (bool, optional)",
		"answer_from_schema": "l2_enabled — булев, необязательный",
	}}
	vars, _, err := ExecuteWith(context.Background(), &f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	assert.Equal(t, "l2_enabled — булев, необязательный", vars["answer"])

	byName := map[string]StepRequest{}
	for _, s := range r.seen {
		byName[s.Name] = s
	}
	// Лимиты РАЗНЫЕ по шагам — то, ради чего понадобилось своё поле:
	// поиск по коду одна попытка, обход дерева до восьми.
	assert.Equal(t, 1, byName["search_schema"].MaxCalls)
	assert.Equal(t, 8, byName["walk_tree"].MaxCalls)

	// Путь к документации вычислен кодом, а не «выведен» моделью.
	assert.Contains(t, byName["read_doc"].Instruction, "docs/resources/vpc_vip.md")

	// Каждый шаг видел ровно свой инструмент.
	assert.Equal(t, []string{"code_search"}, byName["search_schema"].Tools)
	assert.Equal(t, []string{"read_file"}, byName["read_doc"].Tools)
	assert.Equal(t, []string{"list_tree"}, byName["walk_tree"].Tools)
	assert.Empty(t, byName["answer_from_schema"].Tools, "финальный ответ — без инструментов")
}

func TestExampleFlow_FirstHitSkipsFallbacks(t *testing.T) {
	var f Flow
	require.NoError(t, yaml.Unmarshal([]byte(exampleFlow), &f))

	r := &fakeRunner{answer: map[string]string{
		"classify": "internal", "extract": "vpc_vip",
		"search_schema":      "l2_enabled bool optional",
		"answer_from_schema": "готово",
	}}
	_, _, err := ExecuteWith(context.Background(), &f, Deps{Runner: r}, nil)
	require.NoError(t, err)
	for _, s := range r.seen {
		assert.NotEqual(t, "read_doc", s.Name, "нашли сразу — фолбэки не трогаем")
		assert.NotEqual(t, "walk_tree", s.Name)
	}
	assert.Len(t, r.seen, 4, "классификация, извлечение, поиск, ответ — без фолбэков")
}
