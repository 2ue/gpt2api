package image

import "fmt"

// RouteConfig 控制图片路由的运行期开关。
type RouteConfig struct {
	ReverseOnly              bool
	NativeEnabled            bool
	ResponsesFallbackEnabled bool
	ResponsesDirectEnabled   bool
	SafeMode                 bool
}

// ImageRouteConfigProvider 从 settings 读取图片路由开关。
type ImageRouteConfigProvider interface {
	ImageReverseOnly() bool
	ImageNativeEnabled() bool
	ImageResponsesFallbackEnabled() bool
	ImageResponsesDirectEnabled() bool
	ImageSafeMode() bool
}

// RoutePlan 是一次图片请求的执行计划。
type RoutePlan struct {
	Providers []string
}

func SupportLevel(provider string, req CanonicalRequest) string {
	switch provider {
	case ProviderReverse:
		if req.IsMaskedEdit() || req.HasNativeOnlyOptions() || req.NeedsB64JSON() {
			return SupportRejected
		}
		if req.Operation == OperationEdit {
			return SupportEmulated
		}
		return SupportSupported
	case ProviderNative:
		return SupportSupported
	case ProviderResponses:
		if req.IsMaskedEdit() || req.HasNativeOnlyOptions() {
			return SupportRejected
		}
		if req.Operation == OperationEdit || req.N > 1 || req.HasReferenceImages() {
			return SupportEmulated
		}
		return SupportSupported
	default:
		return SupportRejected
	}
}

func PlanRoute(req CanonicalRequest, cfg RouteConfig) (*RoutePlan, error) {
	req.Normalize()
	allowed := make([]string, 0, 3)

	if cfg.ReverseOnly {
		allowed = append(allowed, ProviderReverse)
	} else {
		switch {
		case req.RoutePolicy == RoutePolicyResponses:
			if !cfg.ResponsesDirectEnabled {
				return nil, fmt.Errorf("responses direct route disabled")
			}
			allowed = append(allowed, ProviderResponses)
		case req.RoutePolicy == RoutePolicySafe || cfg.SafeMode:
			if cfg.NativeEnabled {
				allowed = append(allowed, ProviderNative)
			}
			if cfg.ResponsesFallbackEnabled {
				allowed = append(allowed, ProviderResponses)
			}
		default:
			allowed = append(allowed, defaultProviderOrder(req, cfg)...)
		}
	}

	out := make([]string, 0, len(allowed))
	seen := map[string]struct{}{}
	for _, provider := range allowed {
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		if SupportLevel(provider, req) == SupportRejected {
			continue
		}
		out = append(out, provider)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no compatible image provider for request")
	}
	return &RoutePlan{Providers: out}, nil
}

func RouteConfigFromProvider(p ImageRouteConfigProvider) RouteConfig {
	if p == nil {
		return RouteConfig{
			NativeEnabled:            true,
			ResponsesFallbackEnabled: true,
			ResponsesDirectEnabled:   true,
		}
	}
	return RouteConfig{
		ReverseOnly:              p.ImageReverseOnly(),
		NativeEnabled:            p.ImageNativeEnabled(),
		ResponsesFallbackEnabled: p.ImageResponsesFallbackEnabled(),
		ResponsesDirectEnabled:   p.ImageResponsesDirectEnabled(),
		SafeMode:                 p.ImageSafeMode(),
	}
}

func defaultProviderOrder(req CanonicalRequest, cfg RouteConfig) []string {
	providers := make([]string, 0, 3)
	switch {
	case req.IsMaskedEdit() || req.HasNativeOnlyOptions() || req.NeedsB64JSON():
		if cfg.NativeEnabled {
			providers = append(providers, ProviderNative)
		}
		if cfg.ResponsesFallbackEnabled && !req.IsMaskedEdit() && !req.HasNativeOnlyOptions() {
			providers = append(providers, ProviderResponses)
		}
	case req.Operation == OperationEdit:
		if cfg.NativeEnabled {
			providers = append(providers, ProviderNative)
		}
		providers = append(providers, ProviderReverse)
		if cfg.ResponsesFallbackEnabled {
			providers = append(providers, ProviderResponses)
		}
	case req.HasReferenceImages():
		providers = append(providers, ProviderReverse)
		if cfg.NativeEnabled {
			providers = append(providers, ProviderNative)
		}
		if cfg.ResponsesFallbackEnabled {
			providers = append(providers, ProviderResponses)
		}
	default:
		providers = append(providers, ProviderReverse)
		if cfg.NativeEnabled {
			providers = append(providers, ProviderNative)
		}
		if cfg.ResponsesFallbackEnabled {
			providers = append(providers, ProviderResponses)
		}
	}
	return providers
}
