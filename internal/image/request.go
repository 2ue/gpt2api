package image

import (
	"encoding/json"
	"strings"
)

const (
	OperationGenerate = "generate"
	OperationEdit     = "edit"
)

const (
	ResponseFormatURL     = "url"
	ResponseFormatB64JSON = "b64_json"
)

const (
	RoutePolicyAuto      = "auto"
	RoutePolicySafe      = "safe"
	RoutePolicyResponses = "responses"
)

const (
	ProviderReverse   = "reverse"
	ProviderNative    = "native"
	ProviderResponses = "responses_tool"
)

const (
	SupportSupported = "supported"
	SupportEmulated  = "emulated"
	SupportRejected  = "rejected"
)

// InputImage 是 canonical request 内的一张图片输入。
type InputImage struct {
	Data        []byte `json:"-"`
	FileName    string `json:"file_name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// CanonicalRequest 统一表达图片请求语义。
type CanonicalRequest struct {
	Operation         string       `json:"operation"`
	Model             string       `json:"model,omitempty"`
	Prompt            string       `json:"prompt"`
	N                 int          `json:"n"`
	Size              string       `json:"size,omitempty"`
	ResponseFormat    string       `json:"response_format,omitempty"`
	RoutePolicy       string       `json:"route_policy,omitempty"`
	User              string       `json:"user,omitempty"`
	BaseImages        []InputImage `json:"-"`
	ReferenceImages   []InputImage `json:"-"`
	Mask              *InputImage  `json:"-"`
	Quality           string       `json:"quality,omitempty"`
	Style             string       `json:"style,omitempty"`
	Background        string       `json:"background,omitempty"`
	OutputFormat      string       `json:"output_format,omitempty"`
	OutputCompression int          `json:"output_compression,omitempty"`
	Moderation        string       `json:"moderation,omitempty"`
}

func (r *CanonicalRequest) Normalize() {
	if r == nil {
		return
	}
	if r.Operation == "" {
		r.Operation = OperationGenerate
	}
	if r.N <= 0 {
		r.N = 1
	}
	if r.Size == "" {
		r.Size = "1024x1024"
	}
	if r.ResponseFormat == "" {
		r.ResponseFormat = ResponseFormatURL
	}
	if r.RoutePolicy == "" {
		r.RoutePolicy = RoutePolicyAuto
	}
	r.Prompt = strings.TrimSpace(r.Prompt)
	r.Model = strings.TrimSpace(r.Model)
	r.ResponseFormat = strings.ToLower(strings.TrimSpace(r.ResponseFormat))
	r.RoutePolicy = strings.ToLower(strings.TrimSpace(r.RoutePolicy))
	r.Quality = strings.TrimSpace(r.Quality)
	r.Style = strings.TrimSpace(r.Style)
	r.Background = strings.TrimSpace(r.Background)
	r.OutputFormat = strings.TrimSpace(r.OutputFormat)
	r.Moderation = strings.TrimSpace(r.Moderation)
}

func (r CanonicalRequest) NeedsB64JSON() bool {
	return r.ResponseFormat == ResponseFormatB64JSON
}

func (r CanonicalRequest) HasReferenceImages() bool {
	return len(r.ReferenceImages) > 0
}

func (r CanonicalRequest) HasNativeOnlyOptions() bool {
	return r.Quality != "" || r.Style != "" || r.Background != "" ||
		r.OutputFormat != "" || r.OutputCompression > 0 || r.Moderation != ""
}

func (r CanonicalRequest) IsMaskedEdit() bool {
	return r.Operation == OperationEdit && r.Mask != nil
}

func (r CanonicalRequest) RequestOptionsJSON() []byte {
	type payload struct {
		Operation         string `json:"operation"`
		Model             string `json:"model,omitempty"`
		N                 int    `json:"n"`
		Size              string `json:"size,omitempty"`
		ResponseFormat    string `json:"response_format,omitempty"`
		RoutePolicy       string `json:"route_policy,omitempty"`
		BaseImageCount    int    `json:"base_image_count,omitempty"`
		ReferenceCount    int    `json:"reference_count,omitempty"`
		HasMask           bool   `json:"has_mask,omitempty"`
		Quality           string `json:"quality,omitempty"`
		Style             string `json:"style,omitempty"`
		Background        string `json:"background,omitempty"`
		OutputFormat      string `json:"output_format,omitempty"`
		OutputCompression int    `json:"output_compression,omitempty"`
		Moderation        string `json:"moderation,omitempty"`
	}
	b, _ := json.Marshal(payload{
		Operation:         r.Operation,
		Model:             r.Model,
		N:                 r.N,
		Size:              r.Size,
		ResponseFormat:    r.ResponseFormat,
		RoutePolicy:       r.RoutePolicy,
		BaseImageCount:    len(r.BaseImages),
		ReferenceCount:    len(r.ReferenceImages),
		HasMask:           r.Mask != nil,
		Quality:           r.Quality,
		Style:             r.Style,
		Background:        r.Background,
		OutputFormat:      r.OutputFormat,
		OutputCompression: r.OutputCompression,
		Moderation:        r.Moderation,
	})
	return b
}
