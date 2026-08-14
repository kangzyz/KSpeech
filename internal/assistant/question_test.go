package assistant

import "testing"

func TestLooksLikeQuestion(t *testing.T) {
	t.Parallel()
	questions := []string{
		"这块的进度谁跟？",
		"这个方案你怎么看",
		"我们下周三能不能上线",
		"这个方案有没有风险",
		"你吃了吗",
		"什么时候能给到结论",
		"请问接口文档在哪里",
		"how do we ship this",
		"Can you share the numbers",
		// The interview case the auto answer exists for: an imperative request
		// aimed at the listener, with no interrogative word at all.
		"介绍一下你做过的项目",
		"你再展开讲讲这个架构",
		// A caption that states something first and asks at the end.
		"方案我看过了，你觉得这个排期能赶上吗",
		"预算大概多少",
	}
	for _, text := range questions {
		if !LooksLikeQuestion(text) {
			t.Errorf("LooksLikeQuestion(%q) = false, want true", text)
		}
	}

	statements := []string{
		"",
		"嗯",
		"好的，我记一下",
		"这个方案我们已经评审过了",
		"下周三上线，张三负责接口",
		"we shipped the build yesterday",
	}
	for _, text := range statements {
		if LooksLikeQuestion(text) {
			t.Errorf("LooksLikeQuestion(%q) = true, want false", text)
		}
	}
}

// Every line here used to buy a request and push an unrelated answer onto the
// screen: the old classifier accepted any caption that merely contained an
// interrogative word.
func TestLooksLikeQuestionRejectsCommonFalsePositives(t *testing.T) {
	t.Parallel()
	statements := map[string]string{
		"不管怎么样这周都要发版":               "concessive idiom",
		"这块预算多少还是要等财务那边把口径统一之后再确认": "interrogative word inside a long statement",
		"我不知道为什么线上会出现这个报错":          "reported speech",
		"我们先看看能不能把排期提前":             "the speaker's own plan",
		"我来介绍一下我们团队最近做的事情":          "first-person self-announcement",
		"这个多多少少会影响下周的进度":            "idiom containing 多少",
		"无论如何这版都要在周三之前上线":           "concessive idiom",
		"我先说一下背景":                   "first-person request marker",
		"回头问一下他们那边的接口什么时候能好":        "an intention to ask someone else",
		"这些事情我们已经确认过了":              "no marker at all",
	}
	for text, why := range statements {
		if LooksLikeQuestion(text) {
			t.Errorf("LooksLikeQuestion(%q) = true, want false (%s)", text, why)
		}
	}
}

// The punctuation pass appends 问号 from a marker list of its own, so a hedge
// can reach the assistant already marked as a question.
func TestLooksLikeQuestionRechecksAddedQuestionMarks(t *testing.T) {
	t.Parallel()
	if LooksLikeQuestion("不管怎么样？") {
		t.Error("a hedge kept its added 问号 as proof of a question")
	}
	if !LooksLikeQuestion("这个排期能赶上？") {
		t.Error("a marked question without an interrogative word was rejected")
	}
}

func TestParseInsightsStripsMarkersAndEmptyReplies(t *testing.T) {
	t.Parallel()
	got := ParseInsights("1. 下周三评审\n- 接口改由张三负责\n• 预算 12 万\n\n")
	want := []string{"下周三评审", "接口改由张三负责", "预算 12 万"}
	if len(got) != len(want) {
		t.Fatalf("ParseInsights() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("ParseInsights()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
	if points := ParseInsights("无"); len(points) != 0 {
		t.Fatalf("ParseInsights(\"无\") = %#v, want no key points", points)
	}
	if points := ParseInsights("无。\n"); len(points) != 0 {
		t.Fatalf("ParseInsights() kept a punctuated empty marker: %#v", points)
	}
}
