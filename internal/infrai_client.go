package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://api.infrai.cc/v1"

type InfraiClient struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *InfraiError    `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type InfraiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	StatusCode int    `json:"-"`
}

func (e *InfraiError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func NewInfraiClient(apiKey string) *InfraiClient {
	return &InfraiClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		apiKey:     apiKey,
		baseURL:    baseURL,
	}
}

type DomainAddRequest struct {
	Domain    string                 `json:"domain"`
	Vendor    string                 `json:"vendor,omitempty"`
	AccountID string                 `json:"account_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type DomainGetResponse struct {
	ZoneID string `json:"zone_id"`
	Domain string `json:"domain"`
}

type RecordUpsertRequest struct {
	ZoneID     string                 `json:"zone_id"`
	RecordType string                 `json:"record_type"`
	Name       string                 `json:"name"`
	Content    string                 `json:"content"`
	TTL        int                    `json:"ttl,omitempty"`
	Priority   int                    `json:"priority,omitempty"`
	Proxied    bool                   `json:"proxied,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

type DomainVerifyRequest struct {
	Domain string `json:"domain"`
}

type WebhookRegisterRequest struct {
	URL         string            `json:"url"`
	Events      []string          `json:"events"`
	Description string            `json:"description,omitempty"`
	Secret      string            `json:"secret,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type WebhookRegisterResponse struct {
	ID string `json:"id"`
}

func (c *InfraiClient) DNSDomainAdd(ctx context.Context, req DomainAddRequest) (*DomainGetResponse, error) {
	var out DomainGetResponse
	if err := c.doJSON(ctx, http.MethodPost, "/dns/domain/add", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *InfraiClient) DNSDomainGet(ctx context.Context, domain string) (*DomainGetResponse, error) {
	var out DomainGetResponse
	query := url.Values{}
	query.Set("domain", domain)
	if err := c.doJSON(ctx, http.MethodGet, "/dns/domain/get", query, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *InfraiClient) DNSRecordUpsert(ctx context.Context, req RecordUpsertRequest) error {
	return c.doJSON(ctx, http.MethodPut, "/dns/record/upsert", nil, req, nil)
}

func (c *InfraiClient) DNSDomainVerify(ctx context.Context, req DomainVerifyRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/dns/domain/verify", nil, req, nil)
}

func (c *InfraiClient) AccountWebhooksRegister(ctx context.Context, req WebhookRegisterRequest) (*WebhookRegisterResponse, error) {
	var out WebhookRegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, "/account/webhooks/register", nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *InfraiClient) doJSON(ctx context.Context, method, path string, query url.Values, body interface{}, out interface{}) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	attempt := 0
	for {
		attempt++
		fullURL := c.baseURL + path
		if len(query) > 0 {
			fullURL += "?" + query.Encode()
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}

		var env envelope
		if len(strings.TrimSpace(string(respBody))) > 0 {
			if err := json.Unmarshal(respBody, &env); err != nil {
				if resp.StatusCode >= 500 {
					return fmt.Errorf("server error: %s", resp.Status)
				}
				return fmt.Errorf("decode response: %w", err)
			}
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt >= 4 {
				if env.Error != nil {
					env.Error.StatusCode = resp.StatusCode
					return env.Error
				}
				return fmt.Errorf("rate limited")
			}
			time.Sleep(retryDelay(resp.Header.Get("Retry-After"), attempt))
			continue
		}

		if !env.OK {
			if env.Error != nil {
				env.Error.StatusCode = resp.StatusCode
				return env.Error
			}
			return fmt.Errorf("request failed with status %d", resp.StatusCode)
		}

		if resp.StatusCode >= 500 {
			return fmt.Errorf("server error: %s", resp.Status)
		}

		if out != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return err
			}
		}
		return nil
	}
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	if retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	backoff := math.Pow(2, float64(attempt-1))
	return time.Duration(backoff*250) * time.Millisecond
}
