package skillengine

// Generation parameters written outside the block that holds them.

import (
	"fmt"
	"reflect"
	"strings"
)

// misplacedSampling reports the generation parameters set on s, in the order
// the type declares them, and renders the block they belong in.
//
// By REFLECTION over Sampling rather than a list of names, and that is the
// point rather than a shortcut: this check exists because a parameter the
// engine does not read is dropped in silence, and a hand-written list of names
// would recreate that silence for the NEXT parameter added to the type. A list
// has to be remembered; a type cannot be forgotten.
//
// A field counts as set when it is not the zero value, which is why the numbers
// in Sampling are pointers: `temperature: 0` is a deliberate setting and must
// not read as an absent one.
func misplacedSampling(s *Sampling) (names []string, block string) {
	if s == nil {
		return nil, ""
	}
	v := reflect.ValueOf(*s)
	t := v.Type()
	var pairs []string
	for i := range t.NumField() {
		f := v.Field(i)
		if f.IsZero() {
			continue
		}
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
		pairs = append(pairs, fmt.Sprintf("%s: %v", name, deref(f)))
	}
	if len(names) == 0 {
		return nil, ""
	}
	return names, "sampling: {" + strings.Join(pairs, ", ") + "}"
}

func deref(v reflect.Value) any {
	if v.Kind() == reflect.Ptr {
		return v.Elem().Interface()
	}
	return v.Interface()
}

// misplacedSamplingError — the refusal, naming both the parameter and where it
// goes.
//
// It prints the fix with the author's own values in it, because that is what
// gets acted on: the previous version of this diagnosis, coming from a host
// that repaired descriptions against the schema, said "an unknown key was
// removed" — about a key that is perfectly known, just one level down. An
// author told their key is unknown looks for a typo, not for a block.
func misplacedSamplingError(at, where string, s *Sampling) error {
	names, block := misplacedSampling(s)
	if len(names) == 0 {
		return nil
	}
	what := "is a generation parameter and belongs"
	if len(names) > 1 {
		what = "are generation parameters and belong"
	}
	return fmt.Errorf("%s: %s %s inside `%s`, not on the %s itself",
		at, strings.Join(names, ", "), what, block, where)
}
