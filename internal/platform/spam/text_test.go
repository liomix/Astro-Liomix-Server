package spam

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ in, want string }{
		"full width":    {"ｗｅｃｈａｔ１２３", "wechat123"},
		"zero width":    {"赌\u200b场", "赌场"},
		"soft hyphen":   {"ca\u00adsino", "casino"},
		"ideographic":   {"赌场\u3000casino", "赌场 casino"},
		"collapses":     {"  赌场 \n\n casino  ", "赌场 casino"},
		"lowercases":    {"WECHAT", "wechat"},
		"keeps chinese": {"这篇讲得很清楚", "这篇讲得很清楚"},
		"keeps emoji":   {"👍 好", "👍 好"},
	}
	for name, c := range cases {
		if got := normalize(c.in); got != c.want {
			t.Errorf("%s: normalize(%q) = %q, want %q", name, c.in, got, c.want)
		}
	}
}
