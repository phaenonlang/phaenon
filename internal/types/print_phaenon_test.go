package types

import "testing"

func TestPrintPhaenon(t *testing.T) {
	cases := []struct {
		t    Type
		want string
	}{
		{Array{Key: TString, Value: Class{Name: "App\\User"}}, "map<string, App\\User>"},
		{Array{Key: TArrayKey, Value: TInt}, "array<int>"},
		{Array{List: true, Value: TInt}, "list<int>"},
		{ClassString{Of: Class{Name: "App\\User"}}, "class<App\\User>"},
		{ClassString{}, "class"},
		{Union{Types: []Type{Class{Name: "App\\User"}, Null{}}}, "?App\\User"},
		{Union{Types: []Type{TInt, TString, Null{}}}, "int|string|null"},
		{Shape{List: true, Entries: []ShapeEntry{{Type: TInt}, {Type: TString}}}, "(int, string)"},
	}
	for _, c := range cases {
		if got := PrintPhaenon(c.t); got != c.want {
			t.Errorf("PrintPhaenon(%s) = %s, want %s", Print(c.t), got, c.want)
		}
	}
}
