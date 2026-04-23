package gateway

import (
	"strings"
	"testing"

	"github.com/432539/gpt2api/internal/image"
)

func TestBuildChatImageMarkdownUsesOutputCount(t *testing.T) {
	md := buildChatImageMarkdown("task_123", &image.RunResult{
		Outputs: []image.TaskOutput{
			{OutputIndex: 0},
			{OutputIndex: 1},
		},
	})

	if got := strings.Count(md, "![generated]("); got != 2 {
		t.Fatalf("expected 2 markdown images, got %d in %q", got, md)
	}
	if !strings.Contains(md, "/p/img/task_123/1?") {
		t.Fatalf("expected markdown to include second image proxy url, got %q", md)
	}
}
