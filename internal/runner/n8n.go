package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// n8nClient talks to the public API of one judge container.
type n8nClient struct {
	base   string // http://127.0.0.1:<port>
	apiKey string
	hc     *http.Client
}

func newClient(base, apiKey string) *n8nClient {
	return &n8nClient{base: base, apiKey: apiKey, hc: &http.Client{Timeout: 30 * time.Second}}
}

// apiError is a non-2xx answer from n8n, with its message.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("n8n said %d: %s", e.Status, e.Message) }

func (c *n8nClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rd = bytes.NewReader(b)
		default:
			buf, err := json.Marshal(b)
			if err != nil {
				return err
			}
			rd = bytes.NewReader(buf)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-N8N-API-KEY", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Message == "" {
			m.Message = string(data)
		}
		return &apiError{Status: resp.StatusCode, Message: m.Message}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *n8nClient) createCredential(ctx context.Context, name, typ string, data map[string]string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", "/api/v1/credentials", map[string]any{"name": name, "type": typ, "data": data}, &out)
	return out.ID, err
}

func (c *n8nClient) createWorkflow(ctx context.Context, body []byte) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", "/api/v1/workflows", body, &out)
	return out.ID, err
}

func (c *n8nClient) publish(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/api/v1/workflows/"+id+"/publish", map[string]any{}, nil)
}

// execution is the slice of an n8n execution the judge reads.
type execution struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Data   struct {
		ResultData struct {
			Error *struct {
				Message     string `json:"message"`
				Description string `json:"description"`
				Node        *struct {
					Name string `json:"name"`
				} `json:"node"`
			} `json:"error"`
			LastNodeExecuted string                     `json:"lastNodeExecuted"`
			RunData          map[string][]nodeRunRecord `json:"runData"`
		} `json:"resultData"`
	} `json:"data"`
}

type nodeRunRecord struct {
	Data struct {
		Main [][]struct {
			JSON map[string]any `json:"json"`
		} `json:"main"`
	} `json:"data"`
	Error *struct {
		Message     string `json:"message"`
		Description string `json:"description"`
	} `json:"error"`
}

// executions lists recent executions of a workflow with full data.
func (c *n8nClient) executions(ctx context.Context, workflowID string, limit int) ([]execution, error) {
	q := url.Values{}
	q.Set("workflowId", workflowID)
	q.Set("includeData", "true")
	q.Set("limit", fmt.Sprint(limit))
	var out struct {
		Data []execution `json:"data"`
	}
	err := c.do(ctx, "GET", "/api/v1/executions?"+q.Encode(), nil, &out)
	return out.Data, err
}

// createCredentialAny is createCredential for credential data that isn't flat strings (OAuth).
func (c *n8nClient) createCredentialAny(ctx context.Context, name, typ string, data map[string]any) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, "POST", "/api/v1/credentials", map[string]any{"name": name, "type": typ, "data": data}, &out)
	return out.ID, err
}
