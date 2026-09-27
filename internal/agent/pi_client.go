package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxRuntimeResponse = 1 << 20

type RuntimeHTTPError struct {
	StatusCode int
	Message    string
}

func (e *RuntimeHTTPError) Error() string {
	return fmt.Sprintf("agent: Pi HTTP %s: %s", strconv.Itoa(e.StatusCode), e.Message)
}

func IsRuntimeNotFound(err error) bool {
	var httpErr *RuntimeHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

// PiClient calls the project-defined Pi sidecar HTTP protocol. Client should
// have a transport timeout; request contexts remain the authoritative deadline.
type PiClient struct {
	BaseURL      string
	ServiceToken string
	Client       *http.Client
}

// NewPiClient validates and constructs a runtime client.
func NewPiClient(baseURL, serviceToken string, client *http.Client) (*PiClient, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("agent: Pi base URL must be an absolute URL")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &PiClient{BaseURL: parsed.String(), ServiceToken: serviceToken, Client: client}, nil
}

// Start creates a run idempotently. A sidecar may return 200 for an existing
// identical run or 202 for a newly accepted run.
func (c *PiClient) Start(ctx context.Context, request RunRequest) (RunHandle, error) {
	if request.ProtocolVersion == 0 {
		request.ProtocolVersion = ProtocolVersion
	}
	var handle RunHandle
	if err := c.doJSON(ctx, http.MethodPost, "/v1/runs", request, &handle, http.StatusOK, http.StatusAccepted); err != nil {
		return RunHandle{}, err
	}
	return handle, nil
}

// GetRun returns persisted status and the final public result when available.
func (c *PiClient) GetRun(ctx context.Context, runID string) (RunState, error) {
	var state RunState
	err := c.doJSON(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID), nil, &state, http.StatusOK)
	return state, err
}

// Cancel requests idempotent cancellation.
func (c *PiClient) Cancel(ctx context.Context, runID string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/cancel", struct{}{}, nil, http.StatusOK, http.StatusAccepted, http.StatusNoContent)
}

// Health checks process liveness. Readiness remains a separate deployment
// concern because callers should not mistake liveness for model availability.
func (c *PiClient) Health(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/healthz", nil, nil, http.StatusOK)
}

// Ready checks model configuration and session storage availability.
func (c *PiClient) Ready(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/readyz", nil, nil, http.StatusOK)
}

// StreamEvents consumes runtime SSE and calls yield in wire order. lastEventID
// enables replay after an observer disconnect; it never starts another run.
func (c *PiClient) StreamEvents(ctx context.Context, runID, lastEventID string, yield func(RuntimeEvent) error) error {
	if yield == nil {
		return errors.New("agent: event callback is required")
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID)+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("agent: Pi events request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runtimeHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), maxRuntimeResponse)
	var data strings.Builder
	terminal := false
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		var event RuntimeEvent
		if err := json.Unmarshal([]byte(data.String()), &event); err != nil {
			return fmt.Errorf("agent: decode Pi event: %w", err)
		}
		data.Reset()
		terminal = event.Type == "run.completed" || event.Type == "run.failed" || event.Type == "run.cancelled" || event.Type == "run.interrupted"
		return yield(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("agent: read Pi events: %w", err)
	}
	if err := flush(); err != nil {
		return err
	}
	if !terminal {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (c *PiClient) doJSON(ctx context.Context, method, path string, input, output any, expected ...int) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("agent: encode Pi request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("agent: Pi request: %w", err)
	}
	defer resp.Body.Close()
	accepted := false
	for _, status := range expected {
		accepted = accepted || resp.StatusCode == status
	}
	if !accepted {
		return runtimeHTTPError(resp)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxRuntimeResponse))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxRuntimeResponse+1))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("agent: decode Pi response: %w", err)
	}
	return nil
}

func (c *PiClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if c == nil || c.Client == nil || c.BaseURL == "" {
		return nil, errors.New("agent: Pi client is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("agent: build Pi request: %w", err)
	}
	if c.ServiceToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.ServiceToken)
	}
	return req, nil
}

func runtimeHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return &RuntimeHTTPError{StatusCode: resp.StatusCode, Message: message}
}
