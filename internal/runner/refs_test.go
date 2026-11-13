package runner

import "testing"

func TestExpandRefs(t *testing.T) {
	prior := []StepResult{{Body: `{"incident_id":"HSN-1","items":[{"id":"x\"y"}],"n":3}`}}
	cases := []struct {
		in   string
		json bool
		want string
	}{
		{`{"id":"<<step1.incident_id>>"}`, true, `{"id":"HSN-1"}`},
		{`{"id":"<<step1.items[0].id>>"}`, true, `{"id":"x\"y"}`},
		{`<<step1.n>>`, false, `3`},
		{`<<step2.incident_id>>|<<step1.nope>>`, false, `|`},
	}
	for _, c := range cases {
		if got := ExpandRefs(c.in, prior, c.json); got != c.want {
			t.Errorf("%s -> %s, want %s", c.in, got, c.want)
		}
	}
}
