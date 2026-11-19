package types

import "testing"

// ShapesAsPHPStan relaxes two rules and no other: a general array for a
// list shape, and a longer tuple for a shorter one, at any depth of the
// type. What fits under it and not without it is what the checker reports
// as an extra check (design/roadmap.md D33).
func TestShapesAsPHPStan(t *testing.T) {
	at := func(types ...Type) Shape {
		s := Shape{List: true}
		for i, t := range types {
			s.Entries = append(s.Entries, ShapeEntry{IsInt: true, IntKey: int64(i), Type: t})
		}
		return s
	}
	optionalFirst := Shape{List: true, Entries: []ShapeEntry{{IsInt: true, IntKey: 0, Type: TInt, Optional: true}}}
	pair, triple, swapped := at(TInt, TString), at(TInt, TString, TBool), at(TString, TInt)
	byNumber := Array{Key: TInt, Value: TInt}
	list := func(of Type) Array { return Array{Key: TInt, Value: of, List: true} }
	for _, c := range []struct {
		name          string
		arg, param    Type
		here, phpstan bool
	}{
		{"a general array for a list shape", byNumber, optionalFirst, false, true},
		{"a list for it", list(TInt), optionalFirst, true, true},
		{"a general array of other values", Array{Key: TInt, Value: TString}, optionalFirst, false, false},
		{"a general array for an entry that is required", byNumber, at(TInt, TInt), false, false},
		{"a longer tuple", triple, pair, false, true},
		{"a longer tuple in a list", list(triple), list(pair), false, true},
		{"the tuple itself", pair, pair, true, true},
		{"a tuple with other entries", swapped, pair, false, false},
		{"a shorter tuple", at(TInt), pair, false, false},
		{"a longer shape that is unsealed", Shape{List: true, Unsealed: true, Entries: triple.Entries}, pair, false, false},
		{"a general array for a list", byNumber, list(TInt), false, false},
		{"another scalar", TString, TInt, false, false},
	} {
		if got := IsAcceptable(c.arg, c.param, testH); got != c.here {
			t.Errorf("%s: %s for %s is acceptable: %v, want %v", c.name, Print(c.arg), Print(c.param), got, c.here)
		}
		if got := IsAcceptable(c.arg, c.param, ShapesAsPHPStan(testH)); got != c.phpstan {
			t.Errorf("%s: %s for %s is acceptable as PHPStan compares shapes: %v, want %v", c.name, Print(c.arg), Print(c.param), got, c.phpstan)
		}
	}
	if _, twice := ShapesAsPHPStan(ShapesAsPHPStan(testH)).(phpstanShapes).Hierarchy.(phpstanShapes); twice {
		t.Error("ShapesAsPHPStan wraps a hierarchy it has wrapped")
	}
}
