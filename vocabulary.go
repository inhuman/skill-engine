package skillengine

// Vocabulary — the words of the embedding application.
//
// The engine ships NONE of its own, and that is the point. Agents that share
// this format share nothing else: one answers questions about a kitchen, the
// next about a car fleet, the one after that in a language neither of the first
// two used. A word list baked into the engine is one application's habit
// imposed on every other — and imposed INVISIBLY, because a list nobody passed
// in looks exactly like a list that matched nothing.
//
// The rule the engine keeps for itself is narrow: it knows only the words it
// WRITES. A failure it recorded (`ERROR:`, `DENIED:`) it can read back, and the
// working-memory handle it defines (`[mem:id]`) it can recognise. Everything
// beyond that is here, supplied by whoever embeds it.
//
// An empty field is not a mistake. It means "my application has no such words",
// and the mechanism that needed them steps aside — visibly, never by guessing.
type Vocabulary struct {
	// DecisionMarkers — what a model in THIS application writes just before
	// naming its choice: "Result:", "Ответ:", "Résultat:", "结论:".
	//
	// Used by `one_of` to lift a decision out of prose. A step told to answer
	// in one word still writes a paragraph and puts the answer at the end, and
	// the marker is what tells the decision from the enumeration that precedes
	// it ("choose between A and B… Result: B").
	//
	// It is one of five ways `one_of` normalises an answer, and a NARROW one:
	// the other four need no words at all — an exact match, a single value
	// mentioned, a value mentioned strictly more often, and otherwise nothing.
	// Markers therefore decide only a TIE: the allowed values appear equally
	// often and the answer is prose. Leaving this empty never yields a WRONG
	// value — only, in that tie, an empty one, which is the format's designed
	// outcome for "the model did not decide".
	//
	// When that happens the step's trace names this field, so a missing
	// declaration is visible rather than inferred from a quiet default.
	DecisionMarkers []string

	// TruncationNotes — how THIS application marks a result it shortened:
	// "truncated:", "обрезано:", "gekürzt:". Written at the start of the note
	// the host appends on the last line, e.g. "…\n[truncated: 42kb]".
	//
	// The engine strips such a note before a value reaches a tool argument, a
	// loop or a condition — those want the payload, not the host's remark about
	// it. Its own handle marker (`[mem:…]`) is always recognised; this is for
	// the wording that belongs to the application.
	//
	// Left empty by a host that does append notes, the note travels on into
	// call arguments. That is why it is a declaration and not a guess: the
	// engine cannot tell a remark from content.
	TruncationNotes []string
}
