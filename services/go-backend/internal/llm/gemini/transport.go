package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const apiBase = "https://generativelanguage.googleapis.com/v1beta/models/"

type genRequest struct {
	Contents          []genContent `json:"contents"`
	SystemInstruction *genContent  `json:"systemInstruction,omitempty"`
	GenerationConfig  *genConfig   `json:"generationConfig,omitempty"`
}

type genContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []genPart `json:"parts"`
}

type genPart struct {
	Text string `json:"text"`
}

type genConfig struct {
	ResponseMIMEType string  `json:"responseMimeType,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
	MaxOutputTokens  int     `json:"maxOutputTokens,omitempty"`
}

type genResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// generateJSON sends a single prompt and returns the model's raw response text, which callers
// expect to be JSON (responseMimeType is set to application/json). It throttles to respect
// free-tier limits and retries on transient 429/503/transport errors with backoff. It returns
// ErrNotConfigured when no API key is set so callers can fall back to deterministic heuristics.
func (c *Client) generateJSON(ctx context.Context, model, system, prompt string) (string, error) {
	if !c.Configured() {
		return "", ErrNotConfigured
	}
	if strings.TrimSpace(model) == "" {
		model = c.primaryModel()
	}
	reqBody := genRequest{
		Contents: []genContent{{Role: "user", Parts: []genPart{{Text: prompt}}}},
		GenerationConfig: &genConfig{
			ResponseMIMEType: "application/json",
			Temperature:      0.2,
			MaxOutputTokens:  8192,
		},
	}
	if strings.TrimSpace(system) != "" {
		reqBody.SystemInstruction = &genContent{Parts: []genPart{{Text: system}}}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	url := apiBase + model + ":generateContent?key=" + c.apiKey

	var lastErr error
	backoffs := []time.Duration{0, 6 * time.Second, 18 * time.Second}
	for _, wait := range backoffs {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		}
		c.throttle(ctx)
		text, status, err := c.doRequest(ctx, url, payload)
		if err == nil {
			return text, nil
		}
		lastErr = err
		// Only retry transient failures; surface client errors (400/403/404) immediately.
		if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable && status != 0 {
			return "", err
		}
	}
	return "", lastErr
}

// throttle enforces a minimum gap between outbound Gemini calls.
func (c *Client) throttle(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.minGap <= 0 {
		c.lastCall = time.Now()
		return
	}
	if !c.lastCall.IsZero() {
		if elapsed := time.Since(c.lastCall); elapsed < c.minGap {
			select {
			case <-ctx.Done():
			case <-time.After(c.minGap - elapsed):
			}
		}
	}
	c.lastCall = time.Now()
}

func (c *Client) doRequest(ctx context.Context, url string, payload []byte) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("gemini http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed genResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", resp.StatusCode, err
	}
	if parsed.Error != nil {
		return "", parsed.Error.Code, fmt.Errorf("gemini error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return "", resp.StatusCode, errors.New("gemini returned no candidates")
	}
	var sb strings.Builder
	for _, part := range parsed.Candidates[0].Content.Parts {
		sb.WriteString(part.Text)
	}
	return strings.TrimSpace(sb.String()), resp.StatusCode, nil
}

// extractJSON tolerates models that wrap JSON in markdown fences or prose.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return s
	}
	open := s[start]
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}
