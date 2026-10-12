package agentapp

import "testing"

func TestSessionTitleFromText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain Chinese", input: "帮我分析这一集的剧本节奏", want: "帮我分析这一集的剧本节奏"},
		{name: "first non-empty markdown line", input: "\n  ##  修改贾母正房油灯的视觉设定  \nMore", want: "修改贾母正房油灯的视觉设定"},
		{name: "collapses whitespace", input: "Review   the\tcurrent prompts", want: "Review the current …"},
		{name: "truncates by runes", input: "为黛玉进府设计分镜，并补充镜头运动和人物表演细节", want: "为黛玉进府设计分镜，并补充镜头运动和人…"},
		{name: "empty", input: " \n\t", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := SessionTitleFromText(test.input); got != test.want {
				t.Fatalf("SessionTitleFromText(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestIsDefaultSessionTitle(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "New Session", "new conversation", "New Thread", "新会话", "新对话"} {
		if !IsDefaultSessionTitle(value) {
			t.Errorf("IsDefaultSessionTitle(%q) = false", value)
		}
	}
	if IsDefaultSessionTitle("修改剧本节奏") {
		t.Fatal("meaningful title was treated as a default")
	}
}
