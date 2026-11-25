package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Scheduled HTTP requests (API calls) are a fourth job type next to Commands,
// RunBooks and Terminals. Every run writes one log file that holds the request,
// the response headers and the response body, named exactly like Command logs so
// listing, viewing, download, S3 pointers and retention work unchanged.

const (
	httpRequestJobType               = "http_request"
	httpRequestDefaultTimeoutSeconds = 30
	httpRequestMaxTimeoutSeconds     = 600
	httpRequestDefaultMaxRedirects   = 10
	httpRequestMaxRedirectsLimit     = 50
	httpRequestMaxRetries            = 10
	httpRequestDefaultExpectedStatus = "2xx"
	httpMaskedSecret                 = "••••"
	httpMaskedLogValue               = "****"
)

var httpAllowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
}

var (
	errHTTPRequestSkipped  = errors.New("request already running and skip_if_running is enabled")
	errHTTPRequestShutdown = errors.New("server is shutting down")
)

// HTTPKeyValue is one row of a Postman-style key/value table (headers, query
// parameters, form fields). Rows keep their order and may be disabled.
type HTTPKeyValue struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled *bool  `json:"enabled,omitempty"` // nil means enabled
}

func (kv HTTPKeyValue) isEnabled() bool { return kv.Enabled == nil || *kv.Enabled }

// HTTPAuth describes how the request authenticates: none, basic, bearer or api_key.
type HTTPAuth struct {
	Type     string `json:"type"`
	Username string `json:"username,omitempty"` // basic
	Password string `json:"password,omitempty"` // basic (secret)
	Token    string `json:"token,omitempty"`    // bearer (secret)
	Prefix   string `json:"prefix,omitempty"`   // bearer, default "Bearer"
	Key      string `json:"key,omitempty"`      // api_key header or query name
	Value    string `json:"value,omitempty"`    // api_key value (secret)
	AddTo    string `json:"add_to,omitempty"`   // api_key: header (default) or query
}

// HTTPBody is the request payload: none, json, text, xml or form (urlencoded).
type HTTPBody struct {
	Type    string         `json:"type"`
	Content string         `json:"content,omitempty"`
	Fields  []HTTPKeyValue `json:"fields,omitempty"`
}

// HTTPAssertion is one success check evaluated on the final response.
type HTTPAssertion struct {
	Type  string `json:"type"`           // body_contains | json_path_equals | max_response_ms
	Path  string `json:"path,omitempty"` // json_path_equals: dotted path such as data.items.0.id
	Value string `json:"value"`
}

// HTTPRequest is one saved API call in the top-level http_requests array.
type HTTPRequest struct {
	RequestID         string             `json:"request_id"`
	Description       string             `json:"description"`
	Method            string             `json:"method"`
	URL               string             `json:"url"`
	QueryParams       []HTTPKeyValue     `json:"query_params,omitempty"`
	Headers           []HTTPKeyValue     `json:"headers,omitempty"`
	Auth              *HTTPAuth          `json:"auth,omitempty"`
	Body              *HTTPBody          `json:"body,omitempty"`
	TimeoutSeconds    int                `json:"timeout_seconds,omitempty"`
	FollowRedirects   *bool              `json:"follow_redirects,omitempty"`
	MaxRedirects      int                `json:"max_redirects,omitempty"`
	SkipTLSVerify     bool               `json:"skip_tls_verify,omitempty"`
	RetryCount        int                `json:"retry_count,omitempty"`
	RetryDelaySeconds int                `json:"retry_delay_seconds,omitempty"`
	MaxOutputSize     string             `json:"max_output_size,omitempty"`
	ExpectedStatus    string             `json:"expected_status,omitempty"`
	Assertions        []HTTPAssertion    `json:"assertions,omitempty"`
	CronSchedule      string             `json:"cron_schedule,omitempty"`
	BearerToken       string             `json:"bearer_token,omitempty"`
	NotifyWebhook     string             `json:"notify_webhook,omitempty"`
	NotifyEmail       *EmailNotification `json:"notify_email,omitempty"`
	LogStorage        string             `json:"log_storage,omitempty"`
	SkipIfRunning     bool               `json:"skip_if_running,omitempty"`
}

func (r *HTTPRequest) effectiveMethod() string {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		return "GET"
	}
	return method
}

func (r *HTTPRequest) timeout() time.Duration {
	if r.TimeoutSeconds <= 0 {
		return httpRequestDefaultTimeoutSeconds * time.Second
	}
	return time.Duration(r.TimeoutSeconds) * time.Second
}

func (r *HTTPRequest) followRedirects() bool { return r.FollowRedirects == nil || *r.FollowRedirects }

func (r *HTTPRequest) maxRedirects() int {
	if r.MaxRedirects <= 0 {
		return httpRequestDefaultMaxRedirects
	}
	return r.MaxRedirects
}

func (r *HTTPRequest) expectedStatus() string {
	if strings.TrimSpace(r.ExpectedStatus) == "" {
		return httpRequestDefaultExpectedStatus
	}
	return strings.TrimSpace(r.ExpectedStatus)
}

func (r *HTTPRequest) usesS3LogStorage() bool {
	return strings.EqualFold(strings.TrimSpace(r.LogStorage), "s3")
}

func (r *HTTPRequest) summary() string { return r.effectiveMethod() + " " + strings.TrimSpace(r.URL) }

func (r *HTTPRequest) hostName() string {
	if parsed, err := url.Parse(strings.TrimSpace(r.URL)); err == nil {
		return parsed.Host
	}
	return ""
}

func cloneHTTPRequest(value HTTPRequest) HTTPRequest {
	data, _ := json.Marshal(value)
	var clone HTTPRequest
	_ = json.Unmarshal(data, &clone)
	return clone
}

// findHTTPRequest returns a copy of the named request from the (hot-reloaded) config.
func findHTTPRequest(requestID string) (*HTTPRequest, error) {
	if err := cache.loadIfChanged(); err != nil {
		return nil, err
	}
	cache.RLock()
	defer cache.RUnlock()
	if cache.config == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}
	for _, req := range cache.config.HTTPRequests {
		if req.RequestID == requestID {
			copy := cloneHTTPRequest(req)
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("HTTP request '%s' not found", requestID)
}

// normalizeHTTPRequest trims and canonicalises user-supplied fields before validation.
func normalizeHTTPRequest(req *HTTPRequest) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.Method = strings.ToUpper(strings.TrimSpace(req.Method))
	req.URL = strings.TrimSpace(req.URL)
	req.ExpectedStatus = strings.TrimSpace(req.ExpectedStatus)
	req.CronSchedule = strings.TrimSpace(req.CronSchedule)
	req.LogStorage = strings.ToLower(strings.TrimSpace(req.LogStorage))
	if req.Auth != nil {
		req.Auth.Type = strings.ToLower(strings.TrimSpace(req.Auth.Type))
		req.Auth.AddTo = strings.ToLower(strings.TrimSpace(req.Auth.AddTo))
		if req.Auth.Type == "" || req.Auth.Type == "none" {
			req.Auth = nil
		}
	}
	if req.Body != nil {
		req.Body.Type = strings.ToLower(strings.TrimSpace(req.Body.Type))
		if req.Body.Type == "" || req.Body.Type == "none" {
			req.Body = nil
		}
	}
	for i := range req.Assertions {
		req.Assertions[i].Type = strings.ToLower(strings.TrimSpace(req.Assertions[i].Type))
		req.Assertions[i].Path = strings.TrimSpace(req.Assertions[i].Path)
	}
}

// validateHTTPRequests checks every saved request and returns the set of valid IDs.
// Request IDs share the ID namespace with Commands because log file names,
// execution locks, user grants and the Logs page all key off the ID.
func validateHTTPRequests(reqs []HTTPRequest, commandIDs map[string]bool) (map[string]bool, error) {
	ids := make(map[string]bool, len(reqs))
	for _, req := range reqs {
		id := req.RequestID
		if !validateCommandID(id) {
			return nil, fmt.Errorf("http_request ID '%s' must contain only letters, numbers, and hyphens", id)
		}
		if ids[id] {
			return nil, fmt.Errorf("duplicate http_request ID '%s'", id)
		}
		if commandIDs[id] {
			return nil, fmt.Errorf("http_request '%s' conflicts with an existing command ID", id)
		}
		ids[id] = true

		if !httpAllowedMethods[req.effectiveMethod()] {
			return nil, fmt.Errorf("http_request '%s' has unsupported method '%s'", id, req.Method)
		}
		parsed, err := url.Parse(strings.TrimSpace(req.URL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("http_request '%s' must have an absolute http or https URL", id)
		}
		if err := validateHTTPKeyValues(id, "query_params", req.QueryParams); err != nil {
			return nil, err
		}
		if err := validateHTTPKeyValues(id, "headers", req.Headers); err != nil {
			return nil, err
		}
		if req.Auth != nil {
			switch strings.ToLower(req.Auth.Type) {
			case "", "none":
			case "basic":
				if strings.TrimSpace(req.Auth.Username) == "" {
					return nil, fmt.Errorf("http_request '%s' basic auth requires a username", id)
				}
			case "bearer":
				if strings.TrimSpace(req.Auth.Token) == "" {
					return nil, fmt.Errorf("http_request '%s' bearer auth requires a token", id)
				}
			case "api_key":
				if strings.TrimSpace(req.Auth.Key) == "" || req.Auth.Value == "" {
					return nil, fmt.Errorf("http_request '%s' api_key auth requires a key name and value", id)
				}
				addTo := strings.ToLower(req.Auth.AddTo)
				if addTo != "" && addTo != "header" && addTo != "query" {
					return nil, fmt.Errorf("http_request '%s' api_key add_to must be header or query", id)
				}
			default:
				return nil, fmt.Errorf("http_request '%s' has unsupported auth type '%s' (valid: none, basic, bearer, api_key)", id, req.Auth.Type)
			}
		}
		if req.Body != nil {
			switch strings.ToLower(req.Body.Type) {
			case "", "none", "text", "xml":
			case "json":
				if strings.TrimSpace(req.Body.Content) != "" && !json.Valid([]byte(req.Body.Content)) {
					return nil, fmt.Errorf("http_request '%s' json body is not valid JSON", id)
				}
			case "form":
				if err := validateHTTPKeyValues(id, "body.fields", req.Body.Fields); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("http_request '%s' has unsupported body type '%s' (valid: none, json, text, xml, form)", id, req.Body.Type)
			}
		}
		if req.TimeoutSeconds < 0 || req.TimeoutSeconds > httpRequestMaxTimeoutSeconds {
			return nil, fmt.Errorf("http_request '%s' timeout_seconds must be between 0 and %d", id, httpRequestMaxTimeoutSeconds)
		}
		if req.MaxRedirects < 0 || req.MaxRedirects > httpRequestMaxRedirectsLimit {
			return nil, fmt.Errorf("http_request '%s' max_redirects must be between 0 and %d", id, httpRequestMaxRedirectsLimit)
		}
		if req.RetryCount < 0 || req.RetryCount > httpRequestMaxRetries {
			return nil, fmt.Errorf("http_request '%s' retry_count must be between 0 and %d", id, httpRequestMaxRetries)
		}
		if req.RetryDelaySeconds < 0 {
			return nil, fmt.Errorf("http_request '%s' retry_delay_seconds cannot be negative", id)
		}
		if _, err := parseExpectedStatusSpec(req.ExpectedStatus); err != nil {
			return nil, fmt.Errorf("http_request '%s' has invalid expected_status: %v", id, err)
		}
		for index, assertion := range req.Assertions {
			switch strings.ToLower(assertion.Type) {
			case "body_contains":
				if assertion.Value == "" {
					return nil, fmt.Errorf("http_request '%s' assertion %d (body_contains) requires a value", id, index+1)
				}
			case "json_path_equals":
				if strings.TrimSpace(assertion.Path) == "" {
					return nil, fmt.Errorf("http_request '%s' assertion %d (json_path_equals) requires a path", id, index+1)
				}
			case "max_response_ms":
				if value, err := strconv.ParseInt(strings.TrimSpace(assertion.Value), 10, 64); err != nil || value <= 0 {
					return nil, fmt.Errorf("http_request '%s' assertion %d (max_response_ms) requires a positive number of milliseconds", id, index+1)
				}
			default:
				return nil, fmt.Errorf("http_request '%s' assertion %d has unsupported type '%s' (valid: body_contains, json_path_equals, max_response_ms)", id, index+1, assertion.Type)
			}
		}
		if req.MaxOutputSize != "" {
			if _, err := parseSize(req.MaxOutputSize); err != nil {
				return nil, fmt.Errorf("http_request '%s' has invalid max_output_size '%s': %v", id, req.MaxOutputSize, err)
			}
		}
		if err := validateFiveFieldCron(req.CronSchedule); err != nil {
			return nil, fmt.Errorf("http_request '%s' has invalid cron_schedule '%s': %v", id, req.CronSchedule, err)
		}
		if req.LogStorage != "" && !strings.EqualFold(req.LogStorage, "local") && !strings.EqualFold(req.LogStorage, "s3") {
			return nil, fmt.Errorf("http_request '%s' has invalid log_storage '%s' (valid: local, s3)", id, req.LogStorage)
		}
		if req.NotifyEmail != nil && len(req.NotifyEmail.To) == 0 {
			return nil, fmt.Errorf("http_request '%s' email notification requires at least one recipient", id)
		}
	}
	return ids, nil
}

func validateHTTPKeyValues(id, field string, rows []HTTPKeyValue) error {
	for index, row := range rows {
		if row.isEnabled() && strings.TrimSpace(row.Key) == "" {
			return fmt.Errorf("http_request '%s' %s row %d has an empty key", id, field, index+1)
		}
	}
	return nil
}

func httpRequestIDSet(reqs []HTTPRequest) map[string]bool {
	ids := make(map[string]bool, len(reqs))
	for _, req := range reqs {
		ids[req.RequestID] = true
	}
	return ids
}

// ---------------------------------------------------------------------------
// Expected status and assertions
// ---------------------------------------------------------------------------

// parseExpectedStatusSpec accepts "200", "200,201", "2xx", "200-299" and comma-separated mixes.
func parseExpectedStatusSpec(spec string) ([][2]int, error) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" {
		spec = httpRequestDefaultExpectedStatus
	}
	ranges := make([][2]int, 0)
	for _, token := range strings.Split(spec, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		switch {
		case len(token) == 3 && strings.HasSuffix(token, "xx") && token[0] >= '1' && token[0] <= '5':
			base := int(token[0]-'0') * 100
			ranges = append(ranges, [2]int{base, base + 99})
		case strings.Contains(token, "-"):
			parts := strings.SplitN(token, "-", 2)
			low, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			high, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 != nil || err2 != nil || low < 100 || high > 599 || low > high {
				return nil, fmt.Errorf("invalid status range '%s'", token)
			}
			ranges = append(ranges, [2]int{low, high})
		default:
			code, err := strconv.Atoi(token)
			if err != nil || code < 100 || code > 599 {
				return nil, fmt.Errorf("invalid status code '%s'", token)
			}
			ranges = append(ranges, [2]int{code, code})
		}
	}
	if len(ranges) == 0 {
		return nil, fmt.Errorf("expected_status is empty")
	}
	return ranges, nil
}

func matchExpectedStatus(spec string, code int) bool {
	ranges, err := parseExpectedStatusSpec(spec)
	if err != nil {
		return false
	}
	for _, r := range ranges {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

// lookupJSONPath walks a decoded JSON document using a dotted path. Array
// indexes are plain numbers ("items.0.id"); "items[0].id" and "$.a.b" also work.
func lookupJSONPath(doc interface{}, path string) (interface{}, bool) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$")
	path = strings.NewReplacer("[", ".", "]", "").Replace(path)
	current := doc
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		switch value := current.(type) {
		case map[string]interface{}:
			next, ok := value[part]
			if !ok {
				return nil, false
			}
			current = next
		case []interface{}:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func jsonScalarString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		data, _ := json.Marshal(typed)
		return string(data)
	}
}

// HTTPCheckResult is the outcome of the status check or one assertion.
type HTTPCheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

func evaluateHTTPChecks(req *HTTPRequest, result *HTTPExecResult) []HTTPCheckResult {
	checks := make([]HTTPCheckResult, 0, 1+len(req.Assertions))
	spec := req.expectedStatus()
	if matchExpectedStatus(spec, result.StatusCode) {
		checks = append(checks, HTTPCheckResult{Name: "status", Passed: true, Detail: fmt.Sprintf("HTTP %d matches expected %s", result.StatusCode, spec)})
	} else {
		checks = append(checks, HTTPCheckResult{Name: "status", Passed: false, Detail: fmt.Sprintf("HTTP %d not in expected %s", result.StatusCode, spec)})
	}

	var document interface{}
	jsonParsed := false
	var jsonErr error
	for index, assertion := range req.Assertions {
		name := fmt.Sprintf("%s #%d", assertion.Type, index+1)
		switch strings.ToLower(assertion.Type) {
		case "body_contains":
			if strings.Contains(result.Body, assertion.Value) {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: true, Detail: fmt.Sprintf("body contains %q", assertion.Value)})
			} else {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("body does not contain %q", assertion.Value)})
			}
		case "json_path_equals":
			if !jsonParsed {
				jsonErr = json.Unmarshal([]byte(result.Body), &document)
				jsonParsed = true
			}
			if jsonErr != nil {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: false, Detail: "response body is not valid JSON"})
				continue
			}
			value, found := lookupJSONPath(document, assertion.Path)
			if !found {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("path %q not found", assertion.Path)})
				continue
			}
			actual := jsonScalarString(value)
			if actual == assertion.Value {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: true, Detail: fmt.Sprintf("%s == %q", assertion.Path, assertion.Value)})
			} else {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("%s is %q, expected %q", assertion.Path, actual, assertion.Value)})
			}
		case "max_response_ms":
			limit, _ := strconv.ParseInt(strings.TrimSpace(assertion.Value), 10, 64)
			if result.ResponseMS <= limit {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: true, Detail: fmt.Sprintf("responded in %d ms (limit %d ms)", result.ResponseMS, limit)})
			} else {
				checks = append(checks, HTTPCheckResult{Name: name, Passed: false, Detail: fmt.Sprintf("responded in %d ms, slower than %d ms", result.ResponseMS, limit)})
			}
		default:
			checks = append(checks, HTTPCheckResult{Name: name, Passed: false, Detail: "unsupported assertion type"})
		}
	}
	return checks
}

// ---------------------------------------------------------------------------
// Secret masking
// ---------------------------------------------------------------------------

func isSecretHeaderName(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "proxy-authorization", "x-api-key", "x-auth-token", "cookie", "set-cookie":
		return true
	}
	return false
}

func maskedHeaderValue(key, value string) string {
	if !isSecretHeaderName(key) || value == "" {
		return value
	}
	// Keep an auth scheme prefix such as "Bearer" or "Basic" readable.
	if parts := strings.SplitN(value, " ", 2); len(parts) == 2 && strings.EqualFold(strings.TrimSpace(key), "authorization") {
		return parts[0] + " " + httpMaskedLogValue
	}
	return httpMaskedLogValue
}

func maskedHeaderMap(header http.Header) map[string][]string {
	masked := make(map[string][]string, len(header))
	for key, values := range header {
		copied := make([]string, len(values))
		for i, value := range values {
			copied[i] = maskedHeaderValue(key, value)
		}
		masked[key] = copied
	}
	return masked
}

// secretQueryKeys returns the query parameter names whose values must be masked in logs.
func secretQueryKeys(req *HTTPRequest) map[string]bool {
	keys := make(map[string]bool)
	if req.Auth != nil && strings.EqualFold(req.Auth.Type, "api_key") && strings.EqualFold(req.Auth.AddTo, "query") {
		keys[req.Auth.Key] = true
	}
	return keys
}

func maskedURLString(target *url.URL, secretKeys map[string]bool) string {
	if len(secretKeys) == 0 {
		return target.String()
	}
	copied := *target
	query := copied.Query()
	for key := range secretKeys {
		if query.Has(key) {
			query.Set(key, httpMaskedLogValue)
		}
	}
	copied.RawQuery = query.Encode()
	return copied.String()
}

// ---------------------------------------------------------------------------
// Building and executing the request
// ---------------------------------------------------------------------------

func buildHTTPClient(req *HTTPRequest) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if req.SkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit per-request opt-in
	}
	client := &http.Client{Transport: transport, Timeout: req.timeout()}
	if !req.followRedirects() {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	} else {
		limit := req.maxRedirects()
		client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			if len(via) >= limit {
				return fmt.Errorf("stopped after %d redirects", limit)
			}
			return nil
		}
	}
	return client
}

// buildHTTPRequest assembles the outgoing request: URL + query params, body with
// a default Content-Type, user headers (which may override the default) and auth.
func buildHTTPRequest(ctx context.Context, req *HTTPRequest) (*http.Request, []byte, error) {
	target, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid URL: %v", err)
	}
	query := target.Query()
	for _, kv := range req.QueryParams {
		if kv.isEnabled() && strings.TrimSpace(kv.Key) != "" {
			query.Add(kv.Key, kv.Value)
		}
	}
	if req.Auth != nil && strings.EqualFold(req.Auth.Type, "api_key") && strings.EqualFold(req.Auth.AddTo, "query") {
		query.Set(req.Auth.Key, req.Auth.Value)
	}
	target.RawQuery = query.Encode()

	var bodyBytes []byte
	contentType := ""
	if req.Body != nil {
		switch strings.ToLower(req.Body.Type) {
		case "json":
			bodyBytes, contentType = []byte(req.Body.Content), "application/json"
		case "text":
			bodyBytes, contentType = []byte(req.Body.Content), "text/plain; charset=utf-8"
		case "xml":
			bodyBytes, contentType = []byte(req.Body.Content), "application/xml"
		case "form":
			form := url.Values{}
			for _, kv := range req.Body.Fields {
				if kv.isEnabled() && strings.TrimSpace(kv.Key) != "" {
					form.Add(kv.Key, kv.Value)
				}
			}
			bodyBytes, contentType = []byte(form.Encode()), "application/x-www-form-urlencoded"
		}
	}

	var bodyReader io.Reader
	if len(bodyBytes) > 0 {
		bodyReader = bytes.NewReader(bodyBytes)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.effectiveMethod(), target.String(), bodyReader)
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("User-Agent", "CommandGuard/"+AppVersion)
	if contentType != "" && len(bodyBytes) > 0 {
		httpReq.Header.Set("Content-Type", contentType)
	}
	for _, kv := range req.Headers {
		if !kv.isEnabled() || strings.TrimSpace(kv.Key) == "" {
			continue
		}
		key := strings.TrimSpace(kv.Key)
		switch strings.ToLower(key) {
		case "host":
			httpReq.Host = kv.Value
		case "content-type", "user-agent", "content-length":
			httpReq.Header.Set(key, kv.Value)
		default:
			httpReq.Header.Add(key, kv.Value)
		}
	}
	if req.Auth != nil {
		switch strings.ToLower(req.Auth.Type) {
		case "basic":
			httpReq.SetBasicAuth(req.Auth.Username, req.Auth.Password)
		case "bearer":
			prefix := strings.TrimSpace(req.Auth.Prefix)
			if prefix == "" {
				prefix = "Bearer"
			}
			httpReq.Header.Set("Authorization", prefix+" "+req.Auth.Token)
		case "api_key":
			if !strings.EqualFold(req.Auth.AddTo, "query") {
				httpReq.Header.Set(req.Auth.Key, req.Auth.Value)
			}
		}
	}
	return httpReq, bodyBytes, nil
}

func sortedHeaderKeys(header http.Header) []string {
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeHTTPRequestDump(w io.Writer, httpReq *http.Request, body []byte, secretKeys map[string]bool, attempt int) {
	fmt.Fprintf(w, "--- request (attempt %d) ---\n", attempt)
	masked := *httpReq.URL
	if len(secretKeys) > 0 {
		query := masked.Query()
		for key := range secretKeys {
			if query.Has(key) {
				query.Set(key, httpMaskedLogValue)
			}
		}
		masked.RawQuery = query.Encode()
	}
	host := httpReq.Host
	if host == "" {
		host = httpReq.URL.Host
	}
	fmt.Fprintf(w, "%s %s HTTP/1.1\n", httpReq.Method, masked.RequestURI())
	fmt.Fprintf(w, "Host: %s\n", host)
	for _, key := range sortedHeaderKeys(httpReq.Header) {
		for _, value := range httpReq.Header[key] {
			fmt.Fprintf(w, "%s: %s\n", key, maskedHeaderValue(key, value))
		}
	}
	if len(body) > 0 {
		fmt.Fprintf(w, "\n%s\n", body)
	}
}

func writeHTTPResponseDump(w io.Writer, resp *http.Response, attempt int) {
	fmt.Fprintf(w, "--- response headers (attempt %d) ---\n", attempt)
	fmt.Fprintf(w, "%s %s\n", resp.Proto, resp.Status)
	for _, key := range sortedHeaderKeys(resp.Header) {
		for _, value := range resp.Header[key] {
			fmt.Fprintf(w, "%s: %s\n", key, maskedHeaderValue(key, value))
		}
	}
	fmt.Fprintf(w, "--- response body (attempt %d) ---\n", attempt)
}

// HTTPExecResult is what executeHTTPRequest learned from the final attempt.
type HTTPExecResult struct {
	Responded       bool
	StatusCode      int
	Status          string
	Proto           string
	ResponseHeaders http.Header
	Body            string
	BodyBytes       int64
	Truncated       bool
	ResponseMS      int64
	Attempts        int
	Checks          []HTTPCheckResult
	FinalURL        string
}

func waitHTTPRetryDelay(ctx context.Context, seconds int) bool {
	if seconds <= 0 {
		return true
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// executeHTTPRequest sends the request (with retries on transport errors and
// 5xx responses), streams every attempt into logFile, keeps a size-limited
// copy of the final body in memory and evaluates the success checks.
// The returned error is non-nil when the run must count as failed.
func executeHTTPRequest(ctx context.Context, req *HTTPRequest, logFile io.Writer) (*HTTPExecResult, error) {
	result := &HTTPExecResult{}
	maxOutput, err := parseSize(req.MaxOutputSize)
	if err != nil {
		return result, fmt.Errorf("invalid max_output_size: %v", err)
	}
	client := buildHTTPClient(req)
	defer client.CloseIdleConnections()
	secretKeys := secretQueryKeys(req)
	attempts := req.RetryCount + 1

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			reason := "transport error"
			if result.Responded {
				reason = fmt.Sprintf("HTTP %d", result.StatusCode)
			}
			fmt.Fprintf(logFile, "--- retry %d/%d after %ds (previous attempt: %s) ---\n", attempt-1, attempts-1, req.RetryDelaySeconds, reason)
			if !waitHTTPRetryDelay(ctx, req.RetryDelaySeconds) {
				return result, ctx.Err()
			}
		}
		result.Attempts = attempt
		httpReq, body, err := buildHTTPRequest(ctx, req)
		if err != nil {
			return result, err
		}
		writeHTTPRequestDump(logFile, httpReq, body, secretKeys, attempt)

		start := time.Now()
		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			result.Responded, result.StatusCode, result.Status, result.ResponseHeaders = false, 0, "", nil
			result.ResponseMS = time.Since(start).Milliseconds()
			if friendly := describeHTTPTransportError(err, ctx, req, time.Since(start)); friendly != "" {
				lastErr = errors.New(friendly)
				fmt.Fprintf(logFile, "--- response (attempt %d) ---\nERROR: %s\nERROR (detail): %v\n", attempt, friendly, err)
			} else {
				fmt.Fprintf(logFile, "--- response (attempt %d) ---\nERROR: %v\n", attempt, err)
			}
			if ctx.Err() != nil {
				break
			}
			continue
		}

		result.Responded = true
		result.StatusCode, result.Status, result.Proto = resp.StatusCode, resp.Status, resp.Proto
		result.ResponseHeaders = resp.Header.Clone()
		if resp.Request != nil && resp.Request.URL != nil {
			result.FinalURL = maskedURLString(resp.Request.URL, secretKeys)
		}
		writeHTTPResponseDump(logFile, resp, attempt)
		limited := NewLimitedBuffer(maxOutput)
		written, copyErr := io.Copy(io.MultiWriter(logFile, limited), resp.Body)
		resp.Body.Close()
		result.ResponseMS = time.Since(start).Milliseconds()
		result.BodyBytes, result.Body, result.Truncated = written, limited.String(), limited.overflow
		if written > 0 && !strings.HasSuffix(limited.buf.String(), "\n") {
			fmt.Fprintln(logFile)
		}
		if copyErr != nil {
			lastErr = fmt.Errorf("reading response body: %v", copyErr)
			fmt.Fprintf(logFile, "ERROR: %v\n", lastErr)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		lastErr = nil
		if resp.StatusCode >= 500 && attempt < attempts {
			continue
		}
		break
	}

	if !result.Responded {
		detail := "no response"
		if lastErr != nil {
			detail = lastErr.Error()
		}
		if result.Attempts > 1 {
			detail = fmt.Sprintf("%s (no response after %d attempts)", detail, result.Attempts)
		}
		result.Checks = []HTTPCheckResult{{Name: "status", Passed: false, Detail: detail}}
		writeHTTPChecksDump(logFile, result.Checks)
		return result, errors.New(detail)
	}
	if lastErr != nil {
		result.Checks = []HTTPCheckResult{{Name: "status", Passed: false, Detail: lastErr.Error()}}
		writeHTTPChecksDump(logFile, result.Checks)
		return result, lastErr
	}

	result.Checks = evaluateHTTPChecks(req, result)
	writeHTTPChecksDump(logFile, result.Checks)
	failed := make([]string, 0)
	for _, check := range result.Checks {
		if !check.Passed {
			failed = append(failed, check.Detail)
		}
	}
	if len(failed) > 0 {
		return result, errors.New(strings.Join(failed, "; "))
	}
	return result, nil
}

// describeHTTPTransportError turns a timeout into plain language; other errors return "".
func describeHTTPTransportError(err error, ctx context.Context, req *HTTPRequest, elapsed time.Duration) string {
	if err == nil {
		return ""
	}
	if ctx.Err() != nil {
		// The caller's deadline (a RunBook step or RunBook limit) fired, not the request's own timeout.
		return describeTimeout("cancelled", 0, elapsed)
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return describeTimeout("http", int(req.timeout().Seconds()), elapsed)
	}
	return ""
}

func writeHTTPChecksDump(w io.Writer, checks []HTTPCheckResult) {
	fmt.Fprintln(w, "--- checks ---")
	for _, check := range checks {
		verdict := "PASS"
		if !check.Passed {
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "%s %s: %s\n", verdict, check.Name, check.Detail)
	}
}

// ---------------------------------------------------------------------------
// The shared job runner (API handler, cron and RunBook steps all use it)
// ---------------------------------------------------------------------------

type runBookLogContext struct {
	RunBookID string
	RunID     string
	StepID    string
	StepIndex int
	Attempt   int
}

type httpJobParams struct {
	RequestID string
	Request   *HTTPRequest // optional pre-resolved definition (RunBook snapshots)
	TraceID   string
	InvokedBy string
	Trigger   string
	RemoteIP  string
	Agent     string
	LogSkips  bool // write a log file when skip_if_running prevents the run
	RunBook   *runBookLogContext
}

// HTTPRequestOutput is the API response payload for one run.
type HTTPRequestOutput struct {
	RequestID         string              `json:"request_id"`
	Description       string              `json:"description"`
	Method            string              `json:"method"`
	URL               string              `json:"url"`
	Host              string              `json:"host"`
	HTTPStatus        int                 `json:"http_status"`
	StatusText        string              `json:"status_text"`
	ResponseHeaders   map[string][]string `json:"response_headers"`
	Output            string              `json:"output"`
	ResponseBytes     int64               `json:"response_bytes"`
	ResponseMS        int64               `json:"response_ms"`
	ExecTime          string              `json:"exec_time"`
	TraceID           string              `json:"trace_id"`
	IsOutputTruncated int                 `json:"is_output_truncated"`
	ExitCode          int                 `json:"exit_code"`
	Error             string              `json:"error,omitempty"`
	Attempts          int                 `json:"attempts"`
	Checks            []HTTPCheckResult   `json:"checks"`
}

func (o *HTTPRequestOutput) transportFailed() bool {
	return o != nil && o.ExitCode != 0 && o.HTTPStatus == 0
}

func writeHTTPLogHeader(w io.Writer, req *HTTPRequest, p httpJobParams, requestStart time.Time) {
	writeLogField(w, "trace_id", p.TraceID)
	if p.RunBook != nil {
		writeLogField(w, "runbook_id", p.RunBook.RunBookID)
		writeLogField(w, "run_id", p.RunBook.RunID)
		writeLogField(w, "step_id", p.RunBook.StepID)
		writeLogField(w, "step_index", strconv.Itoa(p.RunBook.StepIndex))
		writeLogField(w, "attempt", strconv.Itoa(p.RunBook.Attempt))
	}
	writeLogField(w, "request_received_at", requestStart.Format("2006-01-02 15:04:05.000"))
	writeLogField(w, "cmd_id", req.RequestID)
	writeLogField(w, "job_type", httpRequestJobType)
	writeLogField(w, "request_id", req.RequestID)
	writeLogField(w, "method", req.effectiveMethod())
	writeLogField(w, "url", strings.TrimSpace(req.URL))
	writeLogField(w, "host", req.hostName())
	writeLogField(w, "remote_ip", p.RemoteIP)
	writeLogField(w, "agent", p.Agent)
	writeLogField(w, "invoked_by", p.InvokedBy)
	writeLogField(w, "trigger", p.Trigger)
}

// runHTTPRequestJob executes one saved request end to end: log file, lock,
// active-request tracking, execution, status rewrite, notifications and S3
// finalisation. The returned error is only non-nil when the request did not run
// (unknown ID, skipped, shutdown or log file failure); when it ran, the outcome
// is described by the returned output (exit_code, error, http_status).
func runHTTPRequestJob(ctx context.Context, p httpJobParams) (*HTTPRequestOutput, error) {
	req := p.Request
	if req == nil {
		found, err := findHTTPRequest(p.RequestID)
		if err != nil {
			return nil, err
		}
		req = found
	}
	requestID := req.RequestID
	requestStart := time.Now()
	receivedAt := requestStart.Format("2006-01-02 15:04:05.000")
	output := &HTTPRequestOutput{
		RequestID:   requestID,
		Description: req.Description,
		Method:      req.effectiveMethod(),
		URL:         strings.TrimSpace(req.URL),
		Host:        req.hostName(),
		TraceID:     p.TraceID,
		Checks:      []HTTPCheckResult{},
	}

	skipped := req.SkipIfRunning && execManager.isCommandActive(requestID)
	if skipped && !p.LogSkips {
		return output, errHTTPRequestSkipped
	}

	logFile, logFilename, err := createCommandLogFile(requestID, p.TraceID)
	if err != nil {
		return output, fmt.Errorf("failed to create log file: %v", err)
	}
	defer logFile.Close()
	writeHTTPLogHeader(logFile, req, p, requestStart)

	if skipped {
		writeLogField(logFile, "execution_skipped", "YES (skip_if_running enabled, request already active)")
		writeRunningLogStatus(logFile)
		updateExecutionLogStatus(logFile, logFilename, executionLogStatus{ExecTime: "0s", Error: errHTTPRequestSkipped.Error(), ExitCode: 1})
		finalizeExecutionLog(req.LogStorage, logFile, logFilename)
		output.ExitCode, output.Error = 1, errHTTPRequestSkipped.Error()
		return output, errHTTPRequestSkipped
	}

	lock := execManager.getLock(requestID)
	lock.Lock()
	if appShutdown.IsShuttingDown() {
		lock.Unlock()
		writeLogField(logFile, "execution_skipped", "YES (shutdown in progress)")
		writeRunningLogStatus(logFile)
		updateExecutionLogStatus(logFile, logFilename, executionLogStatus{ExecTime: "0s", Error: errHTTPRequestShutdown.Error(), ExitCode: 1})
		finalizeExecutionLog(req.LogStorage, logFile, logFilename)
		output.ExitCode, output.Error = 1, errHTTPRequestShutdown.Error()
		return output, errHTTPRequestShutdown
	}

	activeRemoteIP := p.RemoteIP
	if p.RunBook != nil {
		activeRemoteIP = "runbook:" + p.RunBook.RunBookID
	}
	execManager.startExecution(&ActiveRequest{TraceID: p.TraceID, CmdID: requestID, Host: output.Host, Command: req.summary(), StartTime: requestStart, RemoteIP: activeRemoteIP})
	defer func() {
		execManager.endExecution(requestID, p.TraceID)
		lock.Unlock()
	}()
	writeRunningLogStatus(logFile)

	start := time.Now()
	result, execErr := executeHTTPRequest(ctx, req, logFile)
	duration := time.Since(start)

	exitCode, errorMsg := 0, ""
	if execErr != nil {
		exitCode, errorMsg = 1, execErr.Error()
	}
	status := completedExecutionLogStatus(duration, result.Truncated, execErr, exitCode)
	status.Extra = []logStatusField{
		{Key: "http_status", Value: strconv.Itoa(result.StatusCode)},
		{Key: "response_ms", Value: strconv.FormatInt(result.ResponseMS, 10)},
		{Key: "response_bytes", Value: strconv.FormatInt(result.BodyBytes, 10)},
		{Key: "attempts", Value: strconv.Itoa(result.Attempts)},
	}
	updateExecutionLogStatus(logFile, logFilename, status)

	execTime := fmt.Sprintf("%.3fs", duration.Seconds())
	webhookData := WebhookData{
		Output: result.Body, ErrorMsg: errorMsg, ExitCode: exitCode, TraceID: p.TraceID, RequestReceivedAt: receivedAt,
		CmdID: requestID, Host: output.Host, Command: req.summary(), RemoteIP: p.RemoteIP, Agent: p.Agent, ExecTime: execTime,
		HTTPStatus: result.StatusCode,
	}
	if req.NotifyWebhook != "" {
		webhookURL := req.NotifyWebhook
		appShutdown.RunAsync("webhook notification "+p.TraceID, func() { sendWebhookNotification(webhookURL, webhookData) })
	}
	if req.NotifyEmail != nil {
		cache.RLock()
		smtp, cfg := cache.config.SMTP, cache.config
		cache.RUnlock()
		if smtp != nil {
			emailData := prepareEmailData(webhookData, cfg, result.Truncated)
			emailData.IncludeOutput = req.NotifyEmail.IncludeOutput
			queueEmail(emailData, req.NotifyEmail, smtp)
		}
	}
	finalizeExecutionLog(req.LogStorage, logFile, logFilename)

	output.HTTPStatus, output.StatusText = result.StatusCode, result.Status
	output.ResponseHeaders = maskedHeaderMap(result.ResponseHeaders)
	if output.ResponseHeaders == nil {
		output.ResponseHeaders = map[string][]string{}
	}
	output.Output, output.ResponseBytes, output.ResponseMS = result.Body, result.BodyBytes, result.ResponseMS
	output.ExecTime, output.ExitCode, output.Error, output.Attempts = execTime, exitCode, errorMsg, result.Attempts
	if result.Truncated {
		output.IsOutputTruncated = 1
	}
	if result.Checks != nil {
		output.Checks = result.Checks
	}
	return output, nil
}

// executeScheduledHTTPRequest is the cron entry point.
func executeScheduledHTTPRequest(requestID string) {
	if !appShutdown.BeginWork() {
		fmt.Printf("[CRON] Skipping HTTP request %s because shutdown is in progress\n", requestID)
		return
	}
	defer appShutdown.DoneWork()
	if err := configRestoreGate.BeginWork(); err != nil {
		fmt.Printf("[CRON] Skipping HTTP request %s: %v\n", requestID, err)
		return
	}
	defer configRestoreGate.EndWork()

	atomic.AddInt64(&totalCronRequests, 1)
	_, err := runHTTPRequestJob(context.Background(), httpJobParams{
		RequestID: requestID, TraceID: generateTraceID(), InvokedBy: "scheduler", Trigger: "schedule",
		RemoteIP: "cron-scheduler", Agent: "cron-scheduler", LogSkips: false,
	})
	if errors.Is(err, errHTTPRequestSkipped) {
		fmt.Printf("[CRON] Skipping %s - already running (skip_if_running enabled)\n", requestID)
	} else if err != nil {
		fmt.Printf("[CRON] HTTP request %s did not run: %v\n", requestID, err)
	}
}

// httpRequestExecHandler serves GET|POST /api/http-requests/{request_id}.
// A JWT (admin, or a user holding the grant) or the request's own bearer token authorises the call.
func httpRequestExecHandler(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&totalWebRequests, 1)
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		sendJSONResponse(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Error: "Method not allowed. Use GET or POST"})
		return
	}
	requestID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/http-requests/"), "/")
	if requestID == "" || strings.Contains(requestID, "/") {
		sendJSONResponse(w, http.StatusBadRequest, APIResponse{Success: false, Error: "Invalid path. Use /api/http-requests/{request_id}"})
		return
	}
	req, err := findHTTPRequest(requestID)
	if err != nil {
		sendJSONResponse(w, http.StatusNotFound, APIResponse{Success: false, Error: fmt.Sprintf("Configuration not found for ID: %s", requestID)})
		return
	}

	claims, jwtAuthenticated := ClaimsFromRequest(r)
	if !jwtAuthenticated {
		authHeader := r.Header.Get("Authorization")
		expectedAuth := "Bearer " + req.BearerToken
		if req.BearerToken == "" || subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedAuth)) != 1 {
			sendJSONResponse(w, http.StatusUnauthorized, APIResponse{Success: false, Error: "Unauthorized: Invalid or missing bearer token"})
			return
		}
	}
	invokedBy, trigger := "api-token", "api"
	if jwtAuthenticated {
		invokedBy, trigger = claims.Username, "manual"
	}

	if !appShutdown.BeginWork() {
		sendJSONResponse(w, http.StatusServiceUnavailable, APIResponse{Success: false, Error: "Server is shutting down"})
		return
	}
	defer appShutdown.DoneWork()
	if err := configRestoreGate.BeginWork(); err != nil {
		sendJSONResponse(w, http.StatusConflict, APIResponse{Success: false, Error: err.Error()})
		return
	}
	defer configRestoreGate.EndWork()

	traceID := generateTraceID()
	output, err := runHTTPRequestJob(context.Background(), httpJobParams{
		RequestID: requestID, Request: req, TraceID: traceID, InvokedBy: invokedBy, Trigger: trigger,
		RemoteIP: getClientIP(r), Agent: r.Header.Get("User-Agent"), LogSkips: true,
	})
	switch {
	case errors.Is(err, errHTTPRequestSkipped):
		sendJSONResponse(w, http.StatusConflict, APIResponse{
			Success: false,
			Error:   fmt.Sprintf("HTTP request '%s' is already running and skip_if_running is enabled", requestID),
			Data:    map[string]interface{}{"trace_id": traceID, "skipped": true, "reason": "skip_if_running"},
		})
	case errors.Is(err, errHTTPRequestShutdown):
		sendJSONResponse(w, http.StatusServiceUnavailable, APIResponse{
			Success: false, Error: "Server is shutting down",
			Data: map[string]interface{}{"trace_id": traceID, "skipped": true, "reason": "shutdown"},
		})
	case err != nil:
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{Success: false, Error: err.Error(), Data: map[string]interface{}{"trace_id": traceID}})
	case output.transportFailed():
		sendJSONResponse(w, http.StatusInternalServerError, APIResponse{Success: false, Error: "Failed to execute HTTP request: " + output.Error, Data: output})
	default:
		sendJSONResponse(w, http.StatusOK, APIResponse{Success: true, Data: output})
	}
}

// ---------------------------------------------------------------------------
// Admin configuration API: /api/config/http-requests[/{id}]
// ---------------------------------------------------------------------------

func decodeHTTPRequests(value interface{}) ([]HTTPRequest, error) {
	if value == nil {
		return []HTTPRequest{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var reqs []HTTPRequest
	if err := json.Unmarshal(data, &reqs); err != nil {
		return nil, err
	}
	return reqs, nil
}

// validateHTTPRequestsRaw validates definitions as they are stored in config.json,
// where URLs, headers and bodies may still contain {{var}} / ${{ENV}} placeholders.
// Substitution is applied to a copy so the raw (unsubstituted) values are what get saved.
func validateHTTPRequestsRaw(reqs []HTTPRequest, variables map[string]interface{}, commandIDs map[string]bool) error {
	data, err := json.Marshal(reqs)
	if err != nil {
		return err
	}
	substituted, err := substituteVariables(string(data), variables)
	if err != nil {
		return err
	}
	var resolved []HTTPRequest
	if err := json.Unmarshal([]byte(substituted), &resolved); err != nil {
		return fmt.Errorf("variable substitution produced invalid JSON: %v", err)
	}
	_, err = validateHTTPRequests(resolved, commandIDs)
	return err
}

func httpRequestIDSetFromConfig(config map[string]interface{}) map[string]bool {
	result := make(map[string]bool)
	items, _ := config["http_requests"].([]interface{})
	for _, value := range items {
		item, _ := value.(map[string]interface{})
		if id, ok := item["request_id"].(string); ok {
			result[id] = true
		}
	}
	return result
}

func runBooksUsingHTTPRequest(value interface{}, requestID string) []string {
	runBooks, _ := decodeRunBooks(value)
	result := make([]string, 0)
	for _, runBook := range runBooks {
		for _, step := range runBook.Steps {
			if step.RequestID == requestID {
				result = append(result, runBook.RunBookID)
				break
			}
		}
	}
	return result
}

// redactHTTPRequest hides auth secrets and secret header values. The trigger
// bearer_token stays visible to administrators (they must configure callers with it),
// matching how Command bearer tokens are shown on the Commands page.
func redactHTTPRequest(req HTTPRequest) map[string]interface{} {
	data, _ := json.Marshal(req)
	var view map[string]interface{}
	_ = json.Unmarshal(data, &view)
	if req.Auth != nil {
		auth := map[string]interface{}{"type": req.Auth.Type}
		if req.Auth.Username != "" {
			auth["username"] = req.Auth.Username
		}
		if req.Auth.Prefix != "" {
			auth["prefix"] = req.Auth.Prefix
		}
		if req.Auth.Key != "" {
			auth["key"] = req.Auth.Key
		}
		if req.Auth.AddTo != "" {
			auth["add_to"] = req.Auth.AddTo
		}
		auth["has_secret"] = req.Auth.Password != "" || req.Auth.Token != "" || req.Auth.Value != ""
		view["auth"] = auth
	}
	if len(req.Headers) > 0 {
		headers := make([]map[string]interface{}, 0, len(req.Headers))
		for _, header := range req.Headers {
			item := map[string]interface{}{"key": header.Key, "value": header.Value}
			if header.Enabled != nil {
				item["enabled"] = *header.Enabled
			}
			if isSecretHeaderName(header.Key) && header.Value != "" {
				item["value"], item["masked"] = httpMaskedSecret, true
			}
			headers = append(headers, item)
		}
		view["headers"] = headers
	}
	view["has_bearer_token"] = req.BearerToken != ""
	return view
}

// preserveHTTPRequestSecrets keeps stored secrets when an update omits or masks them.
func preserveHTTPRequestSecrets(incoming *HTTPRequest, existing HTTPRequest) {
	if incoming.Auth != nil && existing.Auth != nil && strings.EqualFold(incoming.Auth.Type, existing.Auth.Type) {
		keep := func(value string) bool { return value == "" || value == httpMaskedSecret }
		if keep(incoming.Auth.Password) {
			incoming.Auth.Password = existing.Auth.Password
		}
		if keep(incoming.Auth.Token) {
			incoming.Auth.Token = existing.Auth.Token
		}
		if keep(incoming.Auth.Value) {
			incoming.Auth.Value = existing.Auth.Value
		}
	}
	if incoming.BearerToken == httpMaskedSecret {
		incoming.BearerToken = existing.BearerToken
	}
	for i := range incoming.Headers {
		if incoming.Headers[i].Value != httpMaskedSecret {
			continue
		}
		incoming.Headers[i].Value = ""
		seen := 0
		for _, candidate := range incoming.Headers[:i] {
			if strings.EqualFold(candidate.Key, incoming.Headers[i].Key) {
				seen++
			}
		}
		matched := 0
		for _, stored := range existing.Headers {
			if strings.EqualFold(stored.Key, incoming.Headers[i].Key) {
				if matched == seen {
					incoming.Headers[i].Value = stored.Value
					break
				}
				matched++
			}
		}
	}
}

// httpRequestIndex returns the position of requestID in reqs, or -1.
func httpRequestIndex(reqs []HTTPRequest, requestID string) int {
	for i := range reqs {
		if reqs[i].RequestID == requestID {
			return i
		}
	}
	return -1
}

func (ca *ConfigAPI) HandleHTTPRequests(w http.ResponseWriter, r *http.Request) {
	config, err := loadConfigMap(ca.configPath)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
		return
	}
	reqs, err := decodeHTTPRequests(config["http_requests"])
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Invalid HTTP request configuration")
		return
	}
	actor, role := currentActor(r)

	if strings.TrimSuffix(r.URL.Path, "/") == "/api/config/http-requests" {
		switch r.Method {
		case http.MethodGet:
			views := make([]map[string]interface{}, 0, len(reqs))
			for _, req := range reqs {
				views = append(views, redactHTTPRequest(req))
			}
			sendConfigJSONSuccess(w, http.StatusOK, views, "HTTP requests retrieved successfully")
		case http.MethodPost:
			var req HTTPRequest
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req) != nil {
				sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
				return
			}
			normalizeHTTPRequest(&req)
			err := ca.mutateConfig(func(config map[string]interface{}) error {
				reqs, err := decodeHTTPRequests(config["http_requests"])
				if err != nil {
					return httpErr(http.StatusInternalServerError, "Invalid HTTP request configuration")
				}
				for _, existing := range reqs {
					if existing.RequestID == req.RequestID {
						return httpErr(http.StatusConflict, "HTTP request already exists")
					}
				}
				variables, _ := config["variables"].(map[string]interface{})
				candidate := append(append([]HTTPRequest{}, reqs...), req)
				if err := validateHTTPRequestsRaw(candidate, variables, commandIDSetFromConfig(config)); err != nil {
					return httpErr(http.StatusBadRequest, err.Error())
				}
				config["http_requests"] = candidate
				return nil
			})
			if err != nil {
				sendConfigMutationError(w, err, "Failed to save HTTP request")
				return
			}
			writeSecurityAudit(r, actor, role, "http_request_create", req.RequestID, "success", map[string]interface{}{"method": req.effectiveMethod(), "url": req.URL})
			sendConfigJSONSuccess(w, http.StatusCreated, redactHTTPRequest(req), "HTTP request created successfully")
		default:
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	requestID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/config/http-requests/"), "/")
	if requestID == "" || strings.Contains(requestID, "/") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid HTTP request ID")
		return
	}
	index := httpRequestIndex(reqs, requestID)
	if index < 0 {
		sendConfigJSONError(w, http.StatusNotFound, "HTTP request not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		sendConfigJSONSuccess(w, http.StatusOK, redactHTTPRequest(reqs[index]), "HTTP request retrieved successfully")
	case http.MethodPut:
		var req HTTPRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req) != nil {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		normalizeHTTPRequest(&req)
		if req.RequestID == "" {
			req.RequestID = requestID
		}
		if req.RequestID != requestID {
			sendConfigJSONError(w, http.StatusBadRequest, "HTTP request ID cannot be changed")
			return
		}
		err := ca.mutateConfig(func(config map[string]interface{}) error {
			reqs, err := decodeHTTPRequests(config["http_requests"])
			if err != nil {
				return httpErr(http.StatusInternalServerError, "Invalid HTTP request configuration")
			}
			index := httpRequestIndex(reqs, requestID)
			if index < 0 {
				return httpErr(http.StatusNotFound, "HTTP request not found")
			}
			preserveHTTPRequestSecrets(&req, reqs[index])
			variables, _ := config["variables"].(map[string]interface{})
			candidate := append([]HTTPRequest{}, reqs...)
			candidate[index] = req
			if err := validateHTTPRequestsRaw(candidate, variables, commandIDSetFromConfig(config)); err != nil {
				return httpErr(http.StatusBadRequest, err.Error())
			}
			config["http_requests"] = candidate
			return nil
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to save HTTP request")
			return
		}
		writeSecurityAudit(r, actor, role, "http_request_update", requestID, "success", map[string]interface{}{"method": req.effectiveMethod(), "url": req.URL})
		sendConfigJSONSuccess(w, http.StatusOK, redactHTTPRequest(req), "HTTP request updated successfully")
	case http.MethodDelete:
		if execManager.isCommandActive(requestID) {
			sendConfigJSONError(w, http.StatusConflict, "Cannot delete an HTTP request that is currently running")
			return
		}
		err := ca.mutateConfig(func(config map[string]interface{}) error {
			if runBooks := runBooksUsingHTTPRequest(config["runbooks"], requestID); len(runBooks) > 0 {
				return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete HTTP request '%s' because RunBook '%s' uses it", requestID, runBooks[0]))
			}
			if users := usersGranting(config["users"], "http_request", requestID); len(users) > 0 {
				return httpErr(http.StatusConflict, fmt.Sprintf("Cannot delete HTTP request '%s' because it is granted to user '%s'", requestID, users[0]))
			}
			reqs, err := decodeHTTPRequests(config["http_requests"])
			if err != nil {
				return httpErr(http.StatusInternalServerError, "Invalid HTTP request configuration")
			}
			index := httpRequestIndex(reqs, requestID)
			if index < 0 {
				return httpErr(http.StatusNotFound, "HTTP request not found")
			}
			config["http_requests"] = append(append([]HTTPRequest{}, reqs[:index]...), reqs[index+1:]...)
			return nil
		})
		if err != nil {
			sendConfigMutationError(w, err, "Failed to delete HTTP request")
			return
		}
		writeSecurityAudit(r, actor, role, "http_request_delete", requestID, "success", nil)
		sendConfigJSONSuccess(w, http.StatusOK, nil, "HTTP request deleted successfully")
	default:
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// ---------------------------------------------------------------------------
// RunBook step support
// ---------------------------------------------------------------------------

func executeRunBookHTTPAttempt(ctx context.Context, req *HTTPRequest, runBookID, runID string, step RunBookStep, index, attempt int, remoteIP, agent, invokedBy string, limits runBookStepLimits) RunBookAttemptResult {
	started := time.Now()
	result := RunBookAttemptResult{Attempt: attempt, Status: "failed", StartedAt: started}
	if req == nil {
		result.Error = "HTTP request configuration not found"
		result.EndedAt = time.Now()
		return result
	}
	traceID := generateTraceID()
	result.TraceID = traceID
	output, err := runHTTPRequestJob(ctx, httpJobParams{
		RequestID: req.RequestID, Request: req, TraceID: traceID, InvokedBy: invokedBy, Trigger: "runbook",
		RemoteIP: remoteIP, Agent: agent, LogSkips: true,
		RunBook: &runBookLogContext{RunBookID: runBookID, RunID: runID, StepID: step.StepID, StepIndex: index + 1, Attempt: attempt},
	})
	ended := time.Now()
	result.EndedAt, result.Duration = ended, ended.Sub(started).Round(time.Millisecond).String()
	if output != nil {
		result.Output, result.OutputTruncated = output.Output, output.IsOutputTruncated == 1
	}
	if errors.Is(err, errHTTPRequestSkipped) {
		result.Status, result.Error = "skipped", err.Error()
		return result
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if output.ExitCode != 0 {
		result.Error = output.Error
		if ctx.Err() != nil {
			result.Status = "timed_out"
			result.Error = limits.timeoutMessage(ended.Sub(started))
		}
		return result
	}
	result.Status = "succeeded"
	return result
}

// finalizeExecutionLog closes a run's log file and, for S3 log storage, uploads
// it and leaves a local pointer file behind. Commands and HTTP requests share it.
func finalizeExecutionLog(logStorage string, logFile *os.File, logFilename string) {
	if logFile == nil || logFilename == "" {
		return
	}
	if err := logFile.Sync(); err != nil {
		fmt.Printf("[LOG] Failed to sync log file %s: %v\n", logFilename, err)
	}
	if err := logFile.Close(); err != nil {
		fmt.Printf("[LOG] Failed to close log file %s: %v\n", logFilename, err)
	}
	if !strings.EqualFold(strings.TrimSpace(logStorage), "s3") {
		return
	}

	cfg := cache.GetConfig()
	if cfg == nil || cfg.S3 == nil {
		message := fmt.Sprintf("S3 upload skipped for %s: S3 configuration is not set", logFilename)
		fmt.Printf("[S3] %s\n", message)
		logS3Error(message)
		return
	}

	uploadCtx, cancelUpload := appShutdown.contextUntilDeadline()
	defer cancelUpload()

	objectKey, err := uploadLogFileToS3(uploadCtx, cfg.S3, logFilename)
	if err != nil {
		message := fmt.Sprintf("Failed to upload log %s to S3: %v", logFilename, err)
		fmt.Printf("[S3] %s\n", message)
		logS3Error(message)
		return
	}

	fullContent, err := os.ReadFile(logFilename)
	if err != nil {
		message := fmt.Sprintf("Uploaded log %s to s3://%s/%s but failed to read local file for pointer rewrite: %v",
			logFilename, strings.TrimSpace(cfg.S3.Bucket), objectKey, err)
		fmt.Printf("[S3] %s\n", message)
		logS3Error(message)
		return
	}

	if err := writeS3PointerLogFile(logFilename, fullContent, strings.TrimSpace(cfg.S3.Bucket), objectKey); err != nil {
		message := fmt.Sprintf("Uploaded log %s to s3://%s/%s but failed to write local pointer log: %v",
			logFilename, strings.TrimSpace(cfg.S3.Bucket), objectKey, err)
		fmt.Printf("[S3] %s\n", message)
		logS3Error(message)
		return
	}

	fmt.Printf("[S3] Uploaded log %s to s3://%s/%s and wrote local pointer log\n",
		logFilename, strings.TrimSpace(cfg.S3.Bucket), objectKey)
}
