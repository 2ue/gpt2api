package openai

import "testing"

func TestParseResponsesImageResponseIgnoresInputImages(t *testing.T) {
	buf := []byte(`{
		"input": [
			{
				"role": "user",
				"content": [
					{
						"type": "input_image",
						"image_url": "data:image/png;base64,aW5wdXQ="
					},
					{
						"type": "input_text",
						"text": "edit this image"
					}
				]
			}
		],
		"output": [
			{
				"id": "ig_123",
				"type": "image_generation_call",
				"status": "completed",
				"revised_prompt": "A polished version of the prompt.",
				"result": "ZmluYWw="
			}
		]
	}`)

	resp, err := parseResponsesImageResponse(buf)
	if err != nil {
		t.Fatalf("parseResponsesImageResponse() err = %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 generated image, got %d", len(resp.Data))
	}
	if got := resp.Data[0].B64JSON; got != "ZmluYWw=" {
		t.Fatalf("expected generated image payload, got %q", got)
	}
	if got := resp.Data[0].RevisedPrompt; got != "A polished version of the prompt." {
		t.Fatalf("expected revised prompt, got %q", got)
	}
}
