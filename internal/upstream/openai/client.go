package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

type InputImage struct {
	Data        []byte
	FileName    string
	ContentType string
}

type ImageRequest struct {
	Operation         string
	Model             string
	Prompt            string
	N                 int
	Size              string
	ResponseFormat    string
	Quality           string
	Style             string
	Background        string
	OutputFormat      string
	OutputCompression int
	Moderation        string
	BaseImages        []InputImage
	ReferenceImages   []InputImage
	Mask              *InputImage
}

type ImageData struct {
	B64JSON       string
	URL           string
	RevisedPrompt string
}

type ImageResponse struct {
	Data []ImageData
	Raw  map[string]interface{}
}

func New(baseURL, apiKey, proxyURL string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("api key required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	tr := &http.Transport{ForceAttemptHTTP2: true}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		hc:      &http.Client{Timeout: timeout, Transport: tr},
	}, nil
}

func (c *Client) ImagesGenerate(ctx context.Context, req ImageRequest, upstreamModel string) (*ImageResponse, error) {
	body := map[string]interface{}{
		"model":  nonEmpty(upstreamModel, req.Model, "gpt-image-1"),
		"prompt": req.Prompt,
		"n":      req.N,
		"size":   req.Size,
	}
	if req.ResponseFormat != "" {
		body["response_format"] = req.ResponseFormat
	}
	if req.Quality != "" {
		body["quality"] = req.Quality
	}
	if req.Style != "" {
		body["style"] = req.Style
	}
	if req.Background != "" {
		body["background"] = req.Background
	}
	if req.OutputFormat != "" {
		body["output_format"] = req.OutputFormat
	}
	if req.OutputCompression > 0 {
		body["output_compression"] = req.OutputCompression
	}
	if req.Moderation != "" {
		body["moderation"] = req.Moderation
	}
	return c.doJSON(ctx, http.MethodPost, "/images/generations", body)
}

func (c *Client) ImagesEdit(ctx context.Context, req ImageRequest, upstreamModel string) (*ImageResponse, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", nonEmpty(upstreamModel, req.Model, "gpt-image-1"))
	_ = w.WriteField("prompt", req.Prompt)
	_ = w.WriteField("n", fmt.Sprintf("%d", req.N))
	_ = w.WriteField("size", req.Size)
	if req.ResponseFormat != "" {
		_ = w.WriteField("response_format", req.ResponseFormat)
	}
	if req.Quality != "" {
		_ = w.WriteField("quality", req.Quality)
	}
	if req.Style != "" {
		_ = w.WriteField("style", req.Style)
	}
	if req.Background != "" {
		_ = w.WriteField("background", req.Background)
	}
	if req.OutputFormat != "" {
		_ = w.WriteField("output_format", req.OutputFormat)
	}
	if req.OutputCompression > 0 {
		_ = w.WriteField("output_compression", fmt.Sprintf("%d", req.OutputCompression))
	}
	if req.Moderation != "" {
		_ = w.WriteField("moderation", req.Moderation)
	}
	for i, img := range req.BaseImages {
		if err := writeMultipartImage(w, fieldName("image", i), img); err != nil {
			return nil, err
		}
	}
	for i, img := range req.ReferenceImages {
		if err := writeMultipartImage(w, fieldName("image", len(req.BaseImages)+i), img); err != nil {
			return nil, err
		}
	}
	if req.Mask != nil {
		if err := writeMultipartImage(w, "mask", *req.Mask); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return c.doMultipart(ctx, "/images/edits", &buf, w.FormDataContentType())
}

func (c *Client) ResponsesImage(ctx context.Context, req ImageRequest, upstreamModel string) (*ImageResponse, error) {
	inputContent := make([]map[string]interface{}, 0, len(req.BaseImages)+len(req.ReferenceImages)+1)
	for _, img := range req.BaseImages {
		inputContent = append(inputContent, map[string]interface{}{
			"type":      "input_image",
			"image_url": dataURL(img),
		})
	}
	for _, img := range req.ReferenceImages {
		inputContent = append(inputContent, map[string]interface{}{
			"type":      "input_image",
			"image_url": dataURL(img),
		})
	}
	if req.Prompt != "" {
		inputContent = append(inputContent, map[string]interface{}{
			"type": "input_text",
			"text": req.Prompt,
		})
	}
	body := map[string]interface{}{
		"model": nonEmpty(upstreamModel, "gpt-4.1-mini"),
		"input": []map[string]interface{}{
			{
				"role":    "user",
				"content": inputContent,
			},
		},
		"tools": []map[string]interface{}{
			{
				"type": "image_generation",
				"size": req.Size,
			},
		},
	}
	if req.Operation == "edit" {
		body["tool_choice"] = map[string]interface{}{
			"type": "tool",
			"name": "image_generation",
		}
	}
	return c.doResponses(ctx, body)
}

func (c *Client) Download(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, "", fmt.Errorf("download image http=%d", res.StatusCode)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", err
	}
	return b, res.Header.Get("Content-Type"), nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body map[string]interface{}) (*ImageResponse, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("openai images http=%d body=%s", res.StatusCode, truncate(string(buf), 400))
	}
	return parseImageResponse(buf)
}

func (c *Client) doMultipart(ctx context.Context, path string, body *bytes.Buffer, contentType string) (*ImageResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("openai image edit http=%d body=%s", res.StatusCode, truncate(string(buf), 400))
	}
	return parseImageResponse(buf)
}

func (c *Client) doResponses(ctx context.Context, body map[string]interface{}) (*ImageResponse, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("openai responses http=%d body=%s", res.StatusCode, truncate(string(buf), 400))
	}
	return parseResponsesImageResponse(buf)
}

func parseImageResponse(buf []byte) (*ImageResponse, error) {
	var raw struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, err
	}
	out := &ImageResponse{Data: make([]ImageData, 0, len(raw.Data))}
	for _, item := range raw.Data {
		out.Data = append(out.Data, ImageData{
			B64JSON:       item.B64JSON,
			URL:           item.URL,
			RevisedPrompt: item.RevisedPrompt,
		})
	}
	return out, nil
}

func parseResponsesImageResponse(buf []byte) (*ImageResponse, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, err
	}
	out := &ImageResponse{Raw: raw}
	var items []ImageData
	walkResponsesNode(raw, &items)
	out.Data = items
	return out, nil
}

func walkResponsesNode(v interface{}, out *[]ImageData) {
	switch vv := v.(type) {
	case map[string]interface{}:
		if item, ok := responsesImageItem(vv); ok {
			*out = append(*out, item)
		}
		for _, child := range vv {
			walkResponsesNode(child, out)
		}
	case []interface{}:
		for _, child := range vv {
			walkResponsesNode(child, out)
		}
	}
}

func responsesImageItem(node map[string]interface{}) (ImageData, bool) {
	typ, _ := node["type"].(string)
	if typ != "image_generation_call" {
		return ImageData{}, false
	}
	item := ImageData{}
	if s, ok := firstString(node, "result", "b64_json", "image_base64"); ok {
		item.B64JSON = s
	}
	if s, ok := firstString(node, "url"); ok {
		item.URL = s
	}
	if s, ok := firstString(node, "revised_prompt", "prompt"); ok {
		item.RevisedPrompt = s
	}
	if item.B64JSON == "" && item.URL == "" {
		return ImageData{}, false
	}
	return item, true
}

func writeMultipartImage(w *multipart.Writer, field string, img InputImage) error {
	fw, err := w.CreateFormFile(field, nonEmpty(img.FileName, filepath.Base(field)+".png"))
	if err != nil {
		return err
	}
	_, err = fw.Write(img.Data)
	return err
}

func fieldName(base string, idx int) string {
	if idx == 0 {
		return base
	}
	return base + "[]"
}

func dataURL(img InputImage) string {
	ct := img.ContentType
	if ct == "" {
		ct = "image/png"
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
}

func firstString(m map[string]interface{}, keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s, true
			}
		}
	}
	return "", false
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
