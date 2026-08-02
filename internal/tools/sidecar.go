package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type DocumentSidecar struct {
	BaseURL string
	Client  *http.Client
}

func NewDocumentSidecar(baseURL string) *DocumentSidecar {
	return &DocumentSidecar{BaseURL: strings.TrimRight(baseURL, "/"), Client: http.DefaultClient}
}

func (s *DocumentSidecar) Name() string { return "parse_document" }

func (s *DocumentSidecar) Call(ctx context.Context, arguments map[string]any) (Result, error) {
	payload, err := json.Marshal(arguments)
	if err != nil {
		return Result{}, fmt.Errorf("encode document request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/parse", bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("document sidecar request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("document sidecar status: %s", response.Status)
	}
	var output map[string]any
	if err := json.NewDecoder(response.Body).Decode(&output); err != nil {
		return Result{}, fmt.Errorf("decode document response: %w", err)
	}
	return Result{Content: output}, nil
}
