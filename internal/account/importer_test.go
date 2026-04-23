package account

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestParseJSONBlobSupportsSub2APIEmailAddress(t *testing.T) {
	raw := `{
		"type": "sub2api-data",
		"accounts": [
			{
				"name": "chatgpt-demo_example.com",
				"platform": "openai",
				"type": "oauth",
				"credentials": {
					"access_token": "test-token"
				},
				"extra": {
					"email_address": "demo@example.com"
				}
			}
		]
	}`

	items, err := ParseJSONBlob(raw)
	if err != nil {
		t.Fatalf("ParseJSONBlob returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Email != "demo@example.com" {
		t.Fatalf("unexpected email: %q", items[0].Email)
	}
}

func TestParseJSONBlobDerivesSub2APIEmailFromAccessToken(t *testing.T) {
	exp := time.Unix(1735689600, 0).UTC()
	raw := fmt.Sprintf(`{
		"accounts": [
			{
				"name": "openai-fallback",
				"platform": "openai",
				"type": "oauth",
				"credentials": {
					"access_token": %q
				},
				"extra": {}
			}
		]
	}`, testJWT("jwt@example.com", "chatgpt-account", exp.Unix()))

	items, err := ParseJSONBlob(raw)
	if err != nil {
		t.Fatalf("ParseJSONBlob returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Email != "jwt@example.com" {
		t.Fatalf("unexpected email: %q", items[0].Email)
	}
	if items[0].ChatGPTAccountID != "chatgpt-account" {
		t.Fatalf("unexpected account id: %q", items[0].ChatGPTAccountID)
	}
	if items[0].ExpiredAt.Unix() != exp.Unix() {
		t.Fatalf("unexpected expiry: %v", items[0].ExpiredAt)
	}
}

func TestParseJSONBlobAllowsAllSkippedSub2APIAccounts(t *testing.T) {
	raw := `{
		"accounts": [
			{
				"name": "gemini-demo",
				"platform": "gemini",
				"type": "oauth",
				"credentials": {
					"access_token": "test-token"
				}
			}
		]
	}`

	items, err := ParseJSONBlob(raw)
	if err != nil {
		t.Fatalf("ParseJSONBlob returned error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestParseJSONBlobDetailedReportsSkippedSub2APIAccounts(t *testing.T) {
	raw := `{
		"accounts": [
			{
				"name": "gemini-demo",
				"platform": "gemini",
				"type": "oauth",
				"credentials": {
					"access_token": "test-token"
				}
			},
			{
				"name": "openai-missing",
				"platform": "openai",
				"type": "oauth",
				"credentials": {}
			}
		]
	}`

	parsed, err := ParseJSONBlobDetailed(raw)
	if err != nil {
		t.Fatalf("ParseJSONBlobDetailed returned error: %v", err)
	}
	if len(parsed.Sources) != 0 {
		t.Fatalf("expected 0 sources, got %d", len(parsed.Sources))
	}
	if len(parsed.Skipped) != 2 {
		t.Fatalf("expected 2 skipped items, got %d", len(parsed.Skipped))
	}
	if parsed.Skipped[0].Reason != "仅支持 OpenAI 账号" {
		t.Fatalf("unexpected first skip reason: %q", parsed.Skipped[0].Reason)
	}
	if parsed.Skipped[1].Reason != "缺少 access_token" {
		t.Fatalf("unexpected second skip reason: %q", parsed.Skipped[1].Reason)
	}
}

func TestParseJSONBlobDetailedRejectsMalformedTrailingJSON(t *testing.T) {
	raw := `{"accounts":[{"name":"gemini-demo","platform":"gemini","type":"oauth","credentials":{"access_token":"test-token"}}]}
{"broken":`

	if _, err := ParseJSONBlobDetailed(raw); err == nil {
		t.Fatal("expected malformed trailing JSON to fail")
	}
}

func TestMergeImportSummaryWithSkippedReindexesResults(t *testing.T) {
	summary := &ImportSummary{
		Total:   2,
		Created: 1,
		Updated: 1,
		Results: []ImportLineResult{
			{Index: 0, Email: "created@example.com", Status: "created"},
			{Index: 1, Email: "updated@example.com", Status: "updated"},
		},
	}
	skipped := []ImportLineResult{
		{Index: 0, Email: "skip-a@example.com", Status: "skipped", Reason: "仅支持 OpenAI 账号"},
		{Index: 0, Email: "skip-b@example.com", Status: "skipped", Reason: "缺少 access_token"},
	}

	merged := mergeImportSummaryWithSkipped(summary, skipped)
	if merged.Total != 4 {
		t.Fatalf("expected total 4, got %d", merged.Total)
	}
	if merged.Skipped != 2 {
		t.Fatalf("expected skipped 2, got %d", merged.Skipped)
	}
	if len(merged.Results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(merged.Results))
	}
	for idx, item := range merged.Results {
		if item.Index != idx {
			t.Fatalf("expected result index %d, got %d", idx, item.Index)
		}
	}
}

func testJWT(email, accountID string, exp int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"email":%q,"chatgpt_account_id":%q,"exp":%d}`,
		email,
		accountID,
		exp,
	)))
	return header + "." + payload + ".signature"
}
