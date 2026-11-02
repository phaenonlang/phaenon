package phpversion

import (
	"errors"
	"testing"
)

func TestLowestAdmitted(t *testing.T) {
	cases := map[string]string{
		">=8.5":              "8.5",
		">= 8.5":             "8.5",
		"^8.5":               "8.5",
		"^8.5.1":             "8.5",
		"~8.5.0":             "8.5",
		"~8.5":               "8.5",
		"~8":                 "8.0",
		"8.5.*":              "8.5",
		"8.*":                "8.0",
		"8.5.x":              "8.5",
		"v8.6":               "8.6",
		"8.6.0":              "8.6",
		"==8.6.2":            "8.6",
		"^8.5 || ^9.0":       "8.5",
		"^9.0 || ^8.6":       "8.6",
		"^8.6|^8.5":          "8.5",
		">=8.5 <8.7":         "8.5",
		">=8.5,<8.7":         "8.5",
		">=8.5, <8.7":        "8.5",
		">8.5":               "8.5",
		">8.5 <9":            "8.5",
		"8.5 - 8.6":          "8.5",
		"8.5.2 - 8.6.0":      "8.5",
		">=8.6@dev":          "8.6",
		"^8.6@stable":        "8.6",
		">=8.6.0-dev":        "8.6",
		"^8.6.0-beta1":       "8.6",
		"^8.6.0RC1":          "8.6",
		">=8.6 <8.5 || ^8.5": "8.5",
		"<9 >=8.4":           "8.4",
		"^0.3":               "0.3",
	}
	for in, want := range cases {
		got, err := LowestAdmitted(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got.String() != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
	for _, in := range []string{"*", "<9", "!=8.4", ">=8.5 || <8.0"} {
		if _, err := LowestAdmitted(in); !errors.Is(err, ErrNoBound) {
			t.Errorf("%q: got %v, want ErrNoBound", in, err)
		}
	}
	for _, in := range []string{"", "dev-master", ">=8.6 <8.5", "banana"} {
		if v, err := LowestAdmitted(in); err == nil || errors.Is(err, ErrNoBound) {
			t.Errorf("%q: got %v, %v; want a parse error", in, v, err)
		}
	}
}

func TestParseAndRange(t *testing.T) {
	v, err := Parse("8.6")
	if err != nil || v != PHP86 {
		t.Fatalf("Parse(8.6) = %v, %v", v, err)
	}
	for _, bad := range []string{"8", "8.6.1", "^8.6", "eight"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if !PHP85.Supported() || !PHP86.Supported() || (Version{8, 4}).Supported() || (Version{8, 7}).Supported() {
		t.Error("Supported is not 8.5…8.6")
	}
	if (Version{8, 4}).Clamp() != Floor || (Version{9, 0}).Clamp() != Newest || PHP86.Clamp() != PHP86 {
		t.Error("Clamp")
	}
	if !PHP86.AtLeast(PHP85) || PHP85.AtLeast(PHP86) || !PHP85.Less(PHP86) {
		t.Error("ordering")
	}
}
