package types

import "testing"

// apartH has classes on two branches (User with its final Admin, Order),
// an interface neither Order nor Foo implements, and a generic class of
// each variance.
var apartH = mapHierarchy{
	parents: map[string][]string{
		"user":             {"JsonSerializable"},
		"admin":            {"User"},
		"order":            {},
		"foo":              {},
		"jsonserializable": {},
		"box":              {},
		"source":           {},
		"sink":             {},
	},
	final: map[string]bool{"admin": true, "foo": true},
	iface: map[string]bool{"jsonserializable": true},
	vars:  map[string][]Variance{"source": {Covariant}, "sink": {Contravariant}},
}

func TestNeverSubtype(t *testing.T) {
	list := func(v Type) Type { return Array{Key: TInt, Value: v, List: true} }
	hashmap := func(k, v Type) Type { return Array{Key: k, Value: v} }
	shape := func(entries ...ShapeEntry) Type { return Shape{Entries: entries} }
	of := func(class string, args ...Type) Type { return Class{Name: class, Args: args} }
	sig := func(ret Type, params ...Type) Type {
		c := Callable{HasSig: true, Return: ret}
		for _, p := range params {
			c.Params = append(c.Params, CallableParam{Type: p})
		}
		return c
	}
	user, admin, order, foo, json := NewClass("User"), NewClass("Admin"), NewClass("Order"), NewClass("Foo"), NewClass("JsonSerializable")
	object := Param{Name: "T", Bound: TObject}
	renamed := Param{Name: "TObject", Bound: TObject}
	narrower := Param{Name: "TUser", Bound: user}

	apart := [][2]Type{
		{list(TString), list(TInt)},
		{hashmap(TString, TString), hashmap(TString, TInt)},
		{hashmap(TString, TString), hashmap(TInt, TString)},
		{hashmap(TString, TString), list(TString)},
		{list(TInt), hashmap(TString, TMixed)},
		{hashmap(TString, list(TString)), hashmap(TString, list(TInt))},
		{shape(ShapeEntry{Key: "a", Type: TString}), shape(ShapeEntry{Key: "a", Type: TInt})},
		{shape(ShapeEntry{Key: "a", Type: TInt}), shape(ShapeEntry{Key: "a", Type: TInt}, ShapeEntry{Key: "b", Type: TInt})},
		{shape(ShapeEntry{Key: "a", Type: TString}), hashmap(TString, TInt)},
		{shape(ShapeEntry{Key: "a", Type: TInt}), list(TInt)},
		{hashmap(TString, TString), shape(ShapeEntry{Key: "a", Type: TInt})},
		{list(TInt), shape(ShapeEntry{Key: "a", Type: TInt})},
		{shape(), Array{Key: TString, Value: TInt, NonEmpty: true}},
		{Shape{List: true, Entries: []ShapeEntry{{IsInt: true, IntKey: 0, Type: TString}, {IsInt: true, IntKey: 1, Type: TInt}}},
			Shape{List: true, Entries: []ShapeEntry{{IsInt: true, IntKey: 0, Type: TInt}, {IsInt: true, IntKey: 1, Type: TString}}}},
		{Iterable{TMixed, TString}, Iterable{TMixed, TInt}},
		{hashmap(TInt, user), Iterable{TString, user}},
		{Iterable{TString, user}, list(user)},
		{user, order},
		{list(order), list(user)},
		{list(admin), list(order)},
		{foo, json}, // a final class that does not implement the interface
		{ClassString{order}, ClassString{user}},
		{StringLit{"c"}, MakeUnion(StringLit{"a"}, StringLit{"b"})},
		{StringLit{"abc"}, String{Numeric: true}},
		{Int{20, 30}, Int{0, 10}},
		{TFalse, TTrue},
		{TInt, TString},
		{MakeUnion(TInt, TFloat), MakeUnion(TString, TNull)},
		{of("Box", order), of("Box", user)},
		{of("Box", admin), of("Box", user)}, // an invariant argument is the same type or none
		{of("Box", narrower), of("Box", object)},
		{of("Source", order), of("Source", user)},
		{of("Sink", order), of("Sink", user)},
		{sig(TInt, TString), sig(TString, TInt)},
		{sig(TString, TInt, TInt), sig(TString, TInt)}, // requires an argument its callers do not pass
		{sig(TString, list(TInt)), sig(TString, list(TString))},
	}
	for _, c := range apart {
		if !NeverSubtype(c[0], c[1], apartH) {
			t.Errorf("%s should never be a subtype of %s", Print(c[0]), Print(c[1]))
		}
	}

	mayFit := [][2]Type{
		{list(TInt), list(TInt)},
		{TArray, hashmap(TString, TMixed)}, // a bare array narrowed
		{TArray, shape(ShapeEntry{Key: "a", Type: TInt})},
		{TArray, list(TInt)},
		{hashmap(TArrayKey, TInt), list(TInt)},
		{list(TInt), Array{Key: TInt, Value: TInt, List: true, NonEmpty: true}},
		{hashmap(TString, TMixed), hashmap(TString, TString)},
		{shape(ShapeEntry{Key: "a", Type: TInt}), shape(ShapeEntry{Key: "a", Type: TInt}, ShapeEntry{Key: "b", Type: TInt, Optional: true})},
		{Shape{Unsealed: true, Entries: []ShapeEntry{{Key: "a", Type: TInt}}}, shape(ShapeEntry{Key: "a", Type: TInt}, ShapeEntry{Key: "b", Type: TInt})},
		{TString, StringLit{"a"}},
		{MakeUnion(StringLit{"a"}, StringLit{"b"}), StringLit{"a"}},
		{TInt, Int{0, 10}},
		{Int{0, 10}, Int{5, 20}},
		{ClassString{}, ClassString{user}},
		{TString, ClassString{user}},
		{user, admin}, // the class given may be the subclass expected
		{order, json}, // a subclass of Order may implement it
		{list(json), list(order)},
		{TObject, user},
		{Nullable(list(TString)), Nullable(list(TInt))}, // both may be null
		{MakeUnion(TString, list(TInt)), MakeUnion(TString, list(TString))},
		{TMixed, list(TInt)},
		{list(TMixed), list(TInt)},
		{TBytes, TString},
		{TString, TBytes},
		{TFloat, FloatLit{1.5}},
		{of("Box", object), of("Box", renamed)}, // a type parameter is its bound
		{object, renamed},
		{list(Param{Name: "T"}), list(TInt)},
		{of("Box", TArrayKey), of("Box", TInt)}, // array-key takes either of its members
		{of("Box", TMixed), of("Box", user)},
		{of("Source", user), of("Source", admin)},
		{of("Sink", admin), of("Sink", user)},
		{NewClass("Unknown"), user},
		{Static{}, user},
		{TCallable, sig(TString, TInt)},
		{sig(TFloat, TFloat), sig(TFloat, TInt)}, // an int is passed where a float is taken
		{Callable{HasSig: true, Return: TVoid}, sig(TString)},
		{user, sig(TString)},
		{nil, TInt},
	}
	for _, c := range mayFit {
		if NeverSubtype(c[0], c[1], apartH) {
			t.Errorf("%s may be a subtype of %s", printOrNil(c[0]), printOrNil(c[1]))
		}
	}
}

func printOrNil(t Type) string {
	if t == nil {
		return "<nil>"
	}
	return Print(t)
}
