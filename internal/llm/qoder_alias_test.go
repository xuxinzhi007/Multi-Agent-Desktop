package llm

import "testing"

func TestQoderModelByIDNormalizesLegacyAliases(t *testing.T) {
	cases := map[string]string{
		"DeepSeek-Flash":    "dfmodel",
		"deepseek-flash":    "dfmodel",
		"DeepSeek-V4-Flash": "dfmodel",
		"DeepSeek-Pro":      "dmodel",
		"DeepSeek":          "dmodel",
		"DeepSeek-V4-Pro":   "dmodel",
		"Auto":              "auto",
		"auto":              "auto",
	}
	for in, wantKey := range cases {
		got := qoderModelByID(in)
		if got.key != wantKey {
			t.Errorf("qoderModelByID(%q).key = %q, want %q", in, got.key, wantKey)
		}
	}
}
