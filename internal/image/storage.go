package image

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Storage 负责本地图片产物落盘。
type Storage struct {
	root string
}

func NewStorage(root string) *Storage {
	if strings.TrimSpace(root) == "" {
		root = filepath.Join("data", "image_artifacts")
	}
	return &Storage{root: root}
}

func (s *Storage) Root() string { return s.root }

func (s *Storage) Save(taskID string, idx int, contentType string, body []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("storage not configured")
	}
	ext := extensionForContentType(contentType)
	dir := filepath.Join(s.root, taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%03d%s", idx, ext))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Storage) Read(ref string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	return os.ReadFile(ref)
}

func (s *Storage) ReadBase64(ref string) (string, error) {
	b, err := s.Read(ref)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func extensionForContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
