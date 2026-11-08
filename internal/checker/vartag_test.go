package checker

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/types"
)

// TestRefinesOutside: a @var tag skips the check against PHP's type only
// when it narrows a refinement that leaves that type, never because the
// refined type is mixed.
func TestRefinesOutside(t *testing.T) {
	cases := []struct {
		name                      string
		declared, refined, native types.Type
		want                      bool
	}{
		{"bytes over a PHP string", types.TBytes, types.TBytes, types.TString, true},
		{"a refined mixed excuses nothing", types.TString, types.TMixed, types.TArray, false},
		{"a refinement within PHP's type", types.TString, types.Array{Key: types.TInt, Value: types.TInt, List: true}, types.TArray, false},
		{"a tag outside the refinement", types.TString, types.TBytes, types.TString, false},
	}
	for _, c := range cases {
		if got := refinesOutside(c.declared, c.refined, c.native, nil); got != c.want {
			t.Errorf("%s: refinesOutside = %v, want %v", c.name, got, c.want)
		}
	}
}
