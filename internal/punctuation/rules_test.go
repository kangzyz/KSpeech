package punctuation

import "testing"

func TestRulesClosesSentences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "chinese statement", in: "今天我们讨论一下下个季度的排期", want: "今天我们讨论一下下个季度的排期。"},
		{name: "trailing particle", in: "你听清楚了吗", want: "你听清楚了吗？"},
		{name: "question marker", in: "为什么这个接口这么慢", want: "为什么这个接口这么慢？"},
		{name: "english statement", in: "the build is green", want: "the build is green."},
		{name: "english question", in: "can you hear me", want: "can you hear me?"},
		{name: "mixed script keeps wide mark", in: "版本号是 2025", want: "版本号是 2025。"},
		{name: "surrounding space", in: "  录音已经开始  ", want: "录音已经开始。"},
		{name: "already closed", in: "已经结束了。", want: "已经结束了。"},
		{name: "already a question", in: "是这样吗？", want: "是这样吗？"},
		{name: "clause mark is replaced", in: "先说到这里，", want: "先说到这里。"},
	}
	rules := Rules()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := rules.Punctuate(test.in); got != test.want {
				t.Fatalf("Punctuate(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestRulesLeavesFragmentsAlone(t *testing.T) {
	rules := Rules()
	for _, text := range []string{"", "   ", "嗯", "a"} {
		if got := rules.Punctuate(text); got != text {
			t.Fatalf("Punctuate(%q) = %q, want it unchanged", text, got)
		}
	}
}

// A question word only opens a question when something follows it, so a single
// recognized word is closed as a statement.
func TestRulesIgnoresLoneEnglishOpener(t *testing.T) {
	if got := Rules().Punctuate("is"); got != "is." {
		t.Fatalf("Punctuate(%q) = %q, want a full stop", "is", got)
	}
	if got := Rules().Punctuate("is it"); got != "is it?" {
		t.Fatalf("Punctuate(%q) = %q, want a question mark", "is it", got)
	}
}
