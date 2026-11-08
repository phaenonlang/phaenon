package types

import (
	"strings"
	"testing"
)

// mapHierarchy is a test Hierarchy: parents maps a class to its direct
// supertypes.
type mapHierarchy struct {
	parents map[string][]string
	final   map[string]bool
	vars    map[string][]Variance
}

func (h mapHierarchy) IsSubclassOf(sub, super string) bool {
	if strings.EqualFold(sub, super) {
		return true
	}
	for _, p := range h.parents[strings.ToLower(sub)] {
		if h.IsSubclassOf(p, super) {
			return true
		}
	}
	return false
}
func (h mapHierarchy) IsFinal(name string) bool              { return h.final[strings.ToLower(name)] }
func (h mapHierarchy) IteratorArgs(Class) (Type, Type, bool) { return nil, nil, false }
func (h mapHierarchy) IsInvokable(string) bool               { return false }
func (h mapHierarchy) Variances(name string) []Variance      { return h.vars[strings.ToLower(name)] }
func (h mapHierarchy) IsCallableName(name string) bool       { return false }
func (h mapHierarchy) PropertyType(class, name string) (Type, bool) {
	return nil, false
}
func (h mapHierarchy) Exists(name string) bool {
	_, ok := h.parents[strings.ToLower(name)]
	return ok
}

var testH = mapHierarchy{
	parents: map[string][]string{
		"user":             {"Entity", "JsonSerializable"},
		"admin":            {"User"},
		"entity":           {},
		"jsonserializable": {},
		"foo":              {},
		"closure":          {},
		"traversable":      {},
		"iterator":         {"Traversable"},
		"arrayiterator":    {"Iterator"},
		"collection":       {},
	},
	final: map[string]bool{"admin": true, "foo": true},
	vars:  map[string][]Variance{"collection": {Covariant}},
}

func TestPrint(t *testing.T) {
	cases := []struct {
		t    Type
		want string
	}{
		{TMixed, "mixed"},
		{Nullable(TInt), "int|null"},
		{MakeUnion(TString, TInt, TNull), "int|string|null"},
		{MakeUnion(IntLit(1), IntLit(2)), "int<1, 2>"},
		{MakeUnion(IntLit(1), IntLit(3)), "1|3"},
		{Int{0, 9223372036854775807}, "int<0, max>"},
		{MakeUnion(TTrue, TFalse), "bool"},
		{MakeUnion(TBool, TTrue), "bool"},
		{MakeUnion(StringLit{"a"}, StringLit{"b"}), "'a'|'b'"},
		{MakeUnion(StringLit{"a"}, TString), "string"},
		{Array{Key: TArrayKey, Value: TMixed}, "array"},
		{Array{Key: TString, Value: TInt}, "array<string, int>"},
		{Array{Key: TInt, Value: TInt, List: true}, "list<int>"},
		{Array{Key: TInt, Value: TMixed, List: true, NonEmpty: true}, "non-empty-list"},
		{Shape{Entries: []ShapeEntry{{Key: "a", Type: TInt}, {Key: "b", Type: TString, Optional: true}}}, "array{a: int, b?: string}"},
		{Class{Name: "Foo\\Bar", Args: []Type{TInt}}, "Foo\\Bar<int>"},
		{ClassString{Of: NewClass("Foo")}, "class-string<Foo>"},
		{Callable{HasSig: true, Params: []CallableParam{{Type: TInt}, {Type: TString, Optional: true}}, Return: TBool}, "callable(int, string=): bool"},
		{Callable{Closure: true}, "Closure"},
		{MakeUnion(Intersection{[]Type{NewClass("A"), NewClass("B")}}, TNull), "(A&B)|null"},
		{MakeIntersection(TString, String{NonEmpty: true}), "non-empty-string"},
		{MakeIntersection(TInt, TString), "never"},
		{MakeIntersection(MakeUnion(TInt, TString), TString), "string"},
		{EnumCase{"Suit", "Hearts"}, "Suit::Hearts"},
		{MakeUnion(NewClass("Foo"), EnumCase{"Foo", "A"}), "Foo"},
		{MakeUnion(TMixed, TInt), "mixed"},
		{Iterable{TMixed, TString}, "iterable<string>"},
		{FloatLit{1}, "1.0"},
	}
	for _, c := range cases {
		if got := Print(c.t); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestSubtype(t *testing.T) {
	yes := [][2]Type{
		{IntLit(3), TInt},
		{TInt, TArrayKey},
		{TNever, TInt},
		{TInt, TMixed},
		{TTrue, TBool},
		{TBool, MakeUnion(TTrue, TFalse)},
		{MakeUnion(IntLit(1), IntLit(5)), TInt},
		{Int{0, 10}, MakeUnion(Int{0, 5}, Int{6, 10})},
		{StringLit{"a"}, String{NonEmpty: true}},
		{StringLit{"User"}, ClassString{}},
		{StringLit{"Admin"}, ClassString{NewClass("User")}},
		{ClassString{NewClass("Admin")}, ClassString{NewClass("User")}},
		{ClassString{}, TString},
		{NewClass("Admin"), NewClass("User")},
		{NewClass("Admin"), NewClass("Entity")},
		{NewClass("User"), NewClass("JsonSerializable")},
		{NewClass("User"), TObject},
		{Array{Key: TInt, Value: TInt, List: true}, Array{Key: TArrayKey, Value: TMixed}},
		{Array{Key: TInt, Value: NewClass("Admin"), List: true}, Array{Key: TInt, Value: NewClass("User"), List: true}},
		{Shape{Entries: []ShapeEntry{{Key: "a", Type: TInt}}}, Array{Key: TString, Value: TInt}},
		{Shape{Entries: []ShapeEntry{{Key: "a", Type: TInt}, {Key: "b", Type: TInt}}}, Shape{Entries: []ShapeEntry{{Key: "a", Type: TInt}}, Unsealed: true}},
		{Shape{Entries: []ShapeEntry{{IsInt: true, IntKey: 0, Type: TInt}}}, Array{Key: TInt, Value: TInt, List: true}},
		{Array{Key: TInt, Value: TString}, Iterable{TMixed, TString}},
		{NewClass("ArrayIterator"), TIterable},
		{Callable{Closure: true}, TCallable},
		{Callable{Closure: true}, NewClass("Closure")},
		{Callable{HasSig: true, Params: []CallableParam{{Type: NewClass("User")}}, Return: TInt}, Callable{HasSig: true, Params: []CallableParam{{Type: NewClass("Admin")}}, Return: TArrayKey}},
		{Class{Name: "Collection", Args: []Type{NewClass("Admin")}}, Class{Name: "Collection", Args: []Type{NewClass("User")}}},
		{EnumCase{"Suit", "Hearts"}, NewClass("Suit")},
		{Static{&Class{Name: "User"}}, NewClass("Entity")},
		{Nullable(NewClass("User")), MakeUnion(NewClass("Entity"), TNull)},
	}
	no := [][2]Type{
		{TInt, IntLit(3)},
		{TInt, TFloat},
		{TMixed, TInt},
		{TBool, TTrue},
		{StringLit{""}, String{NonEmpty: true}},
		{StringLit{"Nope"}, ClassString{}},
		{StringLit{"User"}, ClassString{NewClass("Admin")}},
		{TString, ClassString{}},
		{NewClass("User"), NewClass("Admin")},
		{NewClass("Entity"), NewClass("JsonSerializable")},
		{TObject, NewClass("User")},
		{Array{Key: TArrayKey, Value: TMixed}, Array{Key: TInt, Value: TInt, List: true}},
		{Array{Key: TInt, Value: NewClass("User"), List: true}, Array{Key: TInt, Value: NewClass("Admin"), List: true}},
		{Shape{Entries: []ShapeEntry{{Key: "a", Type: TInt}}}, Shape{Entries: []ShapeEntry{{Key: "a", Type: TInt}, {Key: "b", Type: TInt}}}},
		{NewClass("User"), TIterable},
		{TCallable, Callable{Closure: true}},
		{Callable{HasSig: true, Params: []CallableParam{{Type: NewClass("Admin")}}, Return: TInt}, Callable{HasSig: true, Params: []CallableParam{{Type: NewClass("User")}}, Return: TInt}},
		{Class{Name: "Collection", Args: []Type{NewClass("User")}}, Class{Name: "Collection", Args: []Type{NewClass("Admin")}}},
		{Nullable(NewClass("User")), NewClass("User")},
		{TVoid, TMixed},
	}
	for _, c := range yes {
		if !IsSubtype(c[0], c[1], testH) {
			t.Errorf("%s should be a subtype of %s", Print(c[0]), Print(c[1]))
		}
	}
	for _, c := range no {
		if IsSubtype(c[0], c[1], testH) {
			t.Errorf("%s should not be a subtype of %s", Print(c[0]), Print(c[1]))
		}
	}
	if !IsAcceptable(TInt, TFloat, testH) || !IsAcceptable(Nullable(TInt), Nullable(TFloat), testH) {
		t.Error("int should be acceptable for float")
	}
	if IsAcceptable(TFloat, TInt, testH) || IsAcceptable(TString, TFloat, testH) {
		t.Error("float/string should not be acceptable for int/float")
	}
}

func TestNarrow(t *testing.T) {
	cases := []struct {
		got  Type
		want string
	}{
		{Truthy(Nullable(TString)), "non-empty-string"},
		{Truthy(MakeUnion(TInt, TFalse, TNull)), "int<min, -1>|int<1, max>"},
		{Falsy(Nullable(TString)), "''|'0'|null"},
		{Falsy(TInt), "0"},
		{Falsy(Array{Key: TArrayKey, Value: TMixed}), "array{}"},
		{Intersect(Nullable(NewClass("User")), NewClass("Admin"), testH), "Admin"},
		{Intersect(NewClass("Entity"), NewClass("User"), testH), "User"},
		{Intersect(NewClass("Entity"), NewClass("JsonSerializable"), testH), "Entity&JsonSerializable"},
		{Intersect(TMixed, TInt, testH), "int"},
		{Intersect(MakeUnion(TInt, TString, TNull), TScalar, testH), "int|string"},
		{Subtract(Nullable(NewClass("User")), TNull, testH), "User"},
		{Subtract(MakeUnion(NewClass("User"), NewClass("Foo")), NewClass("Entity"), testH), "Foo"},
		{Subtract(TBool, TTrue, testH), "false"},
		{Subtract(Int{0, 3}, IntLit(0), testH), "int<1, 3>"},
		{Subtract(TMixed, TNull, testH), "mixed"},
		{Substitute(Array{Key: TInt, Value: Param{Name: "T"}, List: true}, map[string]Type{"T": NewClass("User")}), "list<User>"},
	}
	for _, c := range cases {
		if got := Print(c.got); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	k, v, ok := IterableKV(MakeUnion(Array{Key: TString, Value: TInt}, Class{Name: "ArrayIterator", Args: []Type{TInt, NewClass("User")}}), testH)
	if !ok || Print(k) != "int|string" || Print(v) != "int|User" {
		t.Errorf("IterableKV: %v %s %s", ok, Print(k), Print(v))
	}
	if _, _, ok := IterableKV(TInt, testH); ok {
		t.Error("int is not iterable")
	}
	if Overlaps(NewClass("Foo"), NewClass("User"), testH) {
		t.Error("final Foo and User do not overlap")
	}
	if !Overlaps(NewClass("Entity"), NewClass("JsonSerializable"), testH) {
		t.Error("two non-final classes may overlap")
	}
	if Overlaps(StringLit{"a"}, StringLit{"b"}, testH) || Overlaps(TInt, TString, testH) {
		t.Error("disjoint literals/kinds must not overlap")
	}
}

func TestNative(t *testing.T) {
	cases := []struct {
		t    Type
		want string
	}{
		{Nullable(NewClass("Foo")), "?\\Foo"},
		{Array{Key: TInt, Value: TInt, List: true}, "array"},
		{MakeUnion(TInt, TString, TNull), "int|string|null"},
		{Param{Name: "T", Bound: NewClass("Foo")}, "\\Foo"},
		{Param{Name: "T"}, "mixed"},
		{Callable{Closure: true, HasSig: true, Return: TInt}, "Closure"},
		{MakeUnion(IntLit(1), IntLit(2)), "int"},
		{Static{&Class{Name: "A"}}, "static"},
	}
	for _, c := range cases {
		got, ok := Native(c.t)
		if !ok || got != c.want {
			t.Errorf("Native(%s) = %q, %v; want %q", Print(c.t), got, ok, c.want)
		}
	}
}

// TestTextBytes: string&bytes, what `is string` makes of bytes
// (design/strings.md §6), is a value of both types, absorbed by either in
// a union, disjoint from what neither is, and a string to PHP.
func TestTextBytes(t *testing.T) {
	tb := TTextBytes
	if got := Print(tb); got != "string&bytes" {
		t.Errorf("Print = %q", got)
	}
	if !Equal(MakeIntersection(TBytes, TString), tb) || !Equal(MakeIntersection(TString, TBytes), tb) {
		t.Errorf("MakeIntersection(string, bytes) = %s", Print(MakeIntersection(TString, TBytes)))
	}
	for _, super := range []Type{TString, TBytes, MakeUnion(TString, TNull), MakeUnion(TString, TBytes), TMixed} {
		if !IsSubtype(tb, super, nil) {
			t.Errorf("string&bytes is not a subtype of %s", Print(super))
		}
	}
	for _, sub := range []Type{TString, TBytes, StringLit{V: ""}} {
		if IsSubtype(sub, tb, nil) {
			t.Errorf("%s is a subtype of string&bytes", Print(sub))
		}
	}
	for _, c := range []struct {
		u    Type
		want string
	}{
		{MakeUnion(tb, TBytes), "bytes"},
		{MakeUnion(TBytes, tb), "bytes"},
		{MakeUnion(tb, TString), "string"},
		{MakeUnion(tb, TNull), "(string&bytes)|null"},
	} {
		if got := Print(c.u); got != c.want {
			t.Errorf("union = %q, want %q", got, c.want)
		}
	}
	for _, y := range []Type{TString, TBytes, StringLit{V: ""}, TMixed} {
		if !Overlaps(tb, y, nil) || !Overlaps(y, tb, nil) {
			t.Errorf("string&bytes does not overlap %s", Print(y))
		}
	}
	for _, y := range []Type{TInt, TNull, NewClass("Foo"), TArray} {
		if Overlaps(tb, y, nil) || Overlaps(y, tb, nil) {
			t.Errorf("string&bytes overlaps %s", Print(y))
		}
	}
	if got := Intersect(tb, TString, nil); !Equal(got, tb) {
		t.Errorf("Intersect(string&bytes, string) = %s", Print(got))
	}
	if got := Intersect(tb, TInt, nil); !Equal(got, TNever) {
		t.Errorf("Intersect(string&bytes, int) = %s", Print(got))
	}
	if got := Intersect(TBytes, TString, nil); !Equal(got, TNever) {
		t.Errorf("Intersect(bytes, string) = %s: is_string() checks no UTF-8", Print(got))
	}
	// Met with more text, it stays one intersection of text and bytes.
	lit := StringLit{V: "x"}
	for _, c := range []struct {
		got  Type
		want string
	}{
		{MakeIntersection(tb, lit), "'x'&bytes"},
		{MakeIntersection(lit, tb), "'x'&bytes"},
		{Intersect(tb, lit, nil), "'x'&bytes"},
		{MakeIntersection(tb, String{NonEmpty: true}), "non-empty-string&bytes"},
		{MakeIntersection(tb, TBytes), "string&bytes"},
		{MakeIntersection(tb, tb), "string&bytes"},
		{MakeIntersection(tb, TInt), "never"},
	} {
		if Print(c.got) != c.want {
			t.Errorf("meet = %s, want %s", Print(c.got), c.want)
		}
		if c.want != "never" && !IsTextBytes(c.got) {
			t.Errorf("%s is not text and bytes", c.want)
		}
	}
	if got := TextSide(MakeIntersection(tb, lit)); !Equal(got, lit) {
		t.Errorf("TextSide = %s", Print(got))
	}
	if got := Erase(tb); !Equal(got, TString) {
		t.Errorf("Erase = %s", Print(got))
	}
	if got, ok := Native(tb); !ok || got != "string" {
		t.Errorf("Native = %q, %v", got, ok)
	}
}
