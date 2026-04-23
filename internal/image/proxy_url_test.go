package image

import "testing"

func TestBuildTaskImageProxyURLsPrefersOutputs(t *testing.T) {
	urls := BuildTaskImageProxyURLs("task_123", []TaskOutput{
		{OutputIndex: 2},
		{OutputIndex: 5},
	}, nil, nil, 0)

	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	if want := BuildImageProxyURL("task_123", 2, 0); urls[0][:len("/p/img/task_123/2")] != want[:len("/p/img/task_123/2")] {
		t.Fatalf("expected first url to target output index 2, got %q", urls[0])
	}
	if want := BuildImageProxyURL("task_123", 5, 0); urls[1][:len("/p/img/task_123/5")] != want[:len("/p/img/task_123/5")] {
		t.Fatalf("expected second url to target output index 5, got %q", urls[1])
	}
}

func TestBuildTaskImageProxyURLsFallsBackToLegacyCount(t *testing.T) {
	urls := BuildTaskImageProxyURLs("task_legacy", nil, []string{"fid_1", "fid_2"}, nil, 0)
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	if got := urls[1]; got[:len("/p/img/task_legacy/1")] != "/p/img/task_legacy/1" {
		t.Fatalf("expected legacy url for index 1, got %q", got)
	}
}
