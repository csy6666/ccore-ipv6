package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.cloudflare.com/client/v4"
const managedComment = "ccore-ipv6"
const defaultWriteInterval = 100 * time.Millisecond

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

type SyncResult struct {
	Created int
	Skipped int
	Failed  int
}

type apiEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type APIError struct {
	Status int
	Errors []apiError
	Raw    string
}

func (e *APIError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("Cloudflare HTTP %d: %s", e.Status, strings.TrimSpace(e.Raw))
	}
	parts := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		parts = append(parts, strconv.Itoa(item.Code)+" "+item.Message)
	}
	return "Cloudflare: " + strings.Join(parts, "; ")
}

func New(token string) (*Client, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("Cloudflare API token is empty")
	}
	baseURL := defaultBaseURL
	if override := strings.TrimSpace(os.Getenv("CCORE_IPV6_CF_ENDPOINT")); override != "" {
		baseURL = strings.TrimRight(override, "/")
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "ccore-ipv6/0.1")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("Cloudflare request failed: %w", err)
			if err := retryWait(ctx, attempt, 0); err != nil {
				return err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if err := retryWait(ctx, attempt, 0); err != nil {
				return err
			}
			continue
		}
		var envelope apiEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return &APIError{Status: resp.StatusCode, Raw: string(raw)}
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &APIError{Status: resp.StatusCode, Errors: envelope.Errors, Raw: string(raw)}
			if err := retryWait(ctx, attempt, retryAfter(resp.Header.Get("Retry-After"))); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
			return &APIError{Status: resp.StatusCode, Errors: envelope.Errors, Raw: string(raw)}
		}
		if out != nil && len(envelope.Result) > 0 {
			if err := json.Unmarshal(envelope.Result, out); err != nil {
				return fmt.Errorf("decode Cloudflare response: %w", err)
			}
		}
		return nil
	}
	return lastErr
}

func retryAfter(raw string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func retryWait(ctx context.Context, attempt int, hint time.Duration) error {
	if hint <= 0 {
		hint = time.Duration(1<<attempt)*400*time.Millisecond + time.Duration(rand.Intn(250))*time.Millisecond
	}
	if hint > 15*time.Second {
		hint = 15 * time.Second
	}
	timer := time.NewTimer(hint)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	for page := 1; page <= 50; page++ {
		path := "/zones?per_page=100&page=" + strconv.Itoa(page)
		var batch []Zone
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) == 0 {
			break
		}
	}
	return all, nil
}

func (c *Client) ListAAAA(ctx context.Context, zoneID string) ([]Record, error) {
	zoneID = url.PathEscape(strings.TrimSpace(zoneID))
	if zoneID == "" {
		return nil, errors.New("zone id is empty")
	}
	var all []Record
	for page := 1; page <= 200; page++ {
		path := "/zones/" + zoneID + "/dns_records?type=AAAA&per_page=100&page=" + strconv.Itoa(page)
		var batch []Record
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) == 0 {
			break
		}
	}
	return all, nil
}

func (c *Client) createAAAA(ctx context.Context, zoneID, name string, address netip.Addr, ttl int, proxied bool) error {
	payload := struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		TTL     int    `json:"ttl"`
		Proxied bool   `json:"proxied"`
		Comment string `json:"comment"`
	}{Type: "AAAA", Name: name, Content: address.String(), TTL: ttl, Proxied: proxied, Comment: managedComment}
	return c.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", payload, nil)
}

func (c *Client) SyncAAAA(ctx context.Context, zoneID string, names []string, addresses []netip.Addr, ttl int, proxied bool) (SyncResult, error) {
	if len(names) != len(addresses) || len(addresses) == 0 {
		return SyncResult{}, errors.New("DNS names and addresses must have the same non-zero length")
	}
	if ttl <= 0 {
		ttl = 60
	}
	existing, err := c.ListAAAA(ctx, zoneID)
	if err != nil {
		return SyncResult{}, err
	}
	have := make(map[string]struct{}, len(existing))
	for _, record := range existing {
		have[recordKey(record.Name, record.Content)] = struct{}{}
	}
	var result SyncResult
	for i, address := range addresses {
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(names[i])), ".")
		key := recordKey(name, address.String())
		if _, ok := have[key]; ok {
			result.Skipped++
			continue
		}
		if result.Created > 0 {
			timer := time.NewTimer(defaultWriteInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		if err := c.createAAAA(ctx, zoneID, name, address, ttl, proxied); err != nil {
			result.Failed++
			return result, fmt.Errorf("create AAAA %s -> %s: %w", name, address, err)
		}
		have[key] = struct{}{}
		result.Created++
	}
	return result, nil
}

func recordKey(name, content string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".")) + "|" + strings.ToLower(strings.TrimSpace(content))
}
