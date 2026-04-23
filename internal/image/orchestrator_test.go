package image

import (
	"fmt"
	"testing"
)

func TestNotifyAPIProviderFailureMarksRateLimit(t *testing.T) {
	rateLimited := 0
	authRequired := 0

	code := notifyAPIProviderFailure(fmt.Errorf("openai images http=429 body=slow down"), func() {
		rateLimited++
	}, func() {
		authRequired++
	})

	if code != ErrRateLimited {
		t.Fatalf("expected %q, got %q", ErrRateLimited, code)
	}
	if rateLimited != 1 || authRequired != 0 {
		t.Fatalf("unexpected callbacks rate=%d auth=%d", rateLimited, authRequired)
	}
}

func TestNotifyAPIProviderFailureMarksAuth(t *testing.T) {
	rateLimited := 0
	authRequired := 0

	code := notifyAPIProviderFailure(fmt.Errorf("openai responses http=401 body=unauthorized"), func() {
		rateLimited++
	}, func() {
		authRequired++
	})

	if code != ErrAuthRequired {
		t.Fatalf("expected %q, got %q", ErrAuthRequired, code)
	}
	if rateLimited != 0 || authRequired != 1 {
		t.Fatalf("unexpected callbacks rate=%d auth=%d", rateLimited, authRequired)
	}
}
