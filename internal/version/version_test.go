package version

import "testing"

func TestConfigVersion_Compare(t *testing.T) {
	cases := []struct {
		name string
		a, b ConfigVersion
		want int
	}{
		{"epoch优先-小", ConfigVersion{0, 999}, ConfigVersion{1, 0}, -1},
		{"epoch优先-大", ConfigVersion{2, 0}, ConfigVersion{1, 999}, 1},
		{"同epoch比gen-小", ConfigVersion{1, 5}, ConfigVersion{1, 6}, -1},
		{"同epoch比gen-大", ConfigVersion{1, 7}, ConfigVersion{1, 6}, 1},
		{"相等", ConfigVersion{1, 6}, ConfigVersion{1, 6}, 0},
		{"零值相等", ConfigVersion{}, ConfigVersion{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.Compare(c.b); got != c.want {
				t.Fatalf("Compare=%d want %d", got, c.want)
			}
			if c.a.Less(c.b) != (c.want < 0) {
				t.Fatalf("Less 与 Compare 不一致")
			}
		})
	}
}
