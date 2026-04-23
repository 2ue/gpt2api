package image

import "testing"

func TestPlanRouteMaskedEditForcesNative(t *testing.T) {
	req := CanonicalRequest{
		Operation:      OperationEdit,
		Prompt:         "edit this",
		N:              1,
		Size:           "1024x1024",
		ResponseFormat: ResponseFormatURL,
		RoutePolicy:    RoutePolicyAuto,
		BaseImages:     []InputImage{{Data: []byte("x")}},
		Mask:           &InputImage{Data: []byte("m")},
	}
	plan, err := PlanRoute(req, RouteConfig{
		NativeEnabled:            true,
		ResponsesFallbackEnabled: true,
		ResponsesDirectEnabled:   true,
	})
	if err != nil {
		t.Fatalf("PlanRoute() err = %v", err)
	}
	if len(plan.Providers) != 1 || plan.Providers[0] != ProviderNative {
		t.Fatalf("expected only native provider, got %#v", plan.Providers)
	}
}

func TestPlanRouteB64JSONExcludesReverse(t *testing.T) {
	req := CanonicalRequest{
		Operation:      OperationGenerate,
		Prompt:         "cat",
		N:              1,
		Size:           "1024x1024",
		ResponseFormat: ResponseFormatB64JSON,
		RoutePolicy:    RoutePolicyAuto,
	}
	plan, err := PlanRoute(req, RouteConfig{
		NativeEnabled:            true,
		ResponsesFallbackEnabled: true,
		ResponsesDirectEnabled:   true,
	})
	if err != nil {
		t.Fatalf("PlanRoute() err = %v", err)
	}
	for _, provider := range plan.Providers {
		if provider == ProviderReverse {
			t.Fatalf("reverse provider should be excluded for b64_json route, got %#v", plan.Providers)
		}
	}
}

func TestPlanRouteResponsesPolicyForcesResponses(t *testing.T) {
	req := CanonicalRequest{
		Operation:      OperationGenerate,
		Prompt:         "dog",
		N:              1,
		Size:           "1024x1024",
		ResponseFormat: ResponseFormatURL,
		RoutePolicy:    RoutePolicyResponses,
	}
	plan, err := PlanRoute(req, RouteConfig{
		NativeEnabled:            true,
		ResponsesFallbackEnabled: true,
		ResponsesDirectEnabled:   true,
	})
	if err != nil {
		t.Fatalf("PlanRoute() err = %v", err)
	}
	if len(plan.Providers) != 1 || plan.Providers[0] != ProviderResponses {
		t.Fatalf("expected only responses provider, got %#v", plan.Providers)
	}
}
