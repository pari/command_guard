package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newHTTPTargetServer returns an in-process target so the tests never touch the network.
func newHTTPTargetServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Target", "ok")
		fmt.Fprint(w, `{"status":"ok","data":{"items":[{"id":7}],"count":1}}`)
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		fmt.Fprint(w, "late")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/json", http.StatusFound)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		payload := map[string]interface{}{
			"method": r.Method, "query": r.URL.Query(), "body": string(body),
			"content_type": r.Header.Get("Content-Type"), "authorization": r.Header.Get("Authorization"),
			"x_api_key": r.Header.Get("X-Api-Key"), "x_custom": r.Header.Get("X-Custom"),
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("0123456789", 50))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func dataMap(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, ok := body["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data object in response: %v", body)
	}
	return data
}

func httpRequestDefinition(id, method, target string) map[string]interface{} {
	return map[string]interface{}{"request_id": id, "method": method, "url": target, "description": "test " + id}
}

func TestMatchExpectedStatus(t *testing.T) {
	cases := []struct {
		spec string
		code int
		want bool
	}{
		{"", 204, true}, {"", 404, false}, {"2xx", 299, true}, {"2xx", 300, false},
		{"200,201", 201, true}, {"200,201", 202, false}, {"200-299,404", 404, true},
		{"3xx, 500", 500, true}, {"abc", 200, false},
	}
	for _, c := range cases {
		if got := matchExpectedStatus(c.spec, c.code); got != c.want {
			t.Errorf("matchExpectedStatus(%q, %d) = %v, want %v", c.spec, c.code, got, c.want)
		}
	}
	for _, bad := range []string{"abc", "600", "99", "300-200", "2xx-3xx"} {
		if _, err := parseExpectedStatusSpec(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestLookupJSONPath(t *testing.T) {
	var doc interface{}
	_ = json.Unmarshal([]byte(`{"data":{"items":[{"id":7,"ok":true}],"name":"x","none":null}}`), &doc)
	cases := map[string]string{
		"data.items.0.id": "7", "data.items[0].ok": "true", "$.data.name": "x", "data.none": "null",
		"data.items": `[{"id":7,"ok":true}]`,
	}
	for path, want := range cases {
		value, found := lookupJSONPath(doc, path)
		if !found {
			t.Errorf("path %q not found", path)
			continue
		}
		if got := jsonScalarString(value); got != want {
			t.Errorf("path %q = %q, want %q", path, got, want)
		}
	}
	for _, missing := range []string{"data.items.5", "data.nope", "data.name.deeper"} {
		if _, found := lookupJSONPath(doc, missing); found {
			t.Errorf("path %q should not be found", missing)
		}
	}
}

func TestValidateHTTPRequests(t *testing.T) {
	good := HTTPRequest{RequestID: "ok-1", Method: "post", URL: "https://example.com/x", ExpectedStatus: "2xx,404",
		Auth: &HTTPAuth{Type: "bearer", Token: "t"}, Body: &HTTPBody{Type: "json", Content: `{"a":1}`},
		Assertions: []HTTPAssertion{{Type: "json_path_equals", Path: "a", Value: "1"}, {Type: "max_response_ms", Value: "500"}}}
	ids, err := validateHTTPRequests([]HTTPRequest{good}, map[string]bool{"cmd-a": true})
	if err != nil || !ids["ok-1"] {
		t.Fatalf("valid request rejected: %v", err)
	}
	bad := []HTTPRequest{
		{RequestID: "bad_id", Method: "GET", URL: "https://example.com"},
		{RequestID: "cmd-a", Method: "GET", URL: "https://example.com"},
		{RequestID: "m", Method: "FETCH", URL: "https://example.com"},
		{RequestID: "u", Method: "GET", URL: "example.com/no-scheme"},
		{RequestID: "u2", Method: "GET", URL: "ftp://example.com"},
		{RequestID: "a", Method: "GET", URL: "https://example.com", Auth: &HTTPAuth{Type: "oauth2"}},
		{RequestID: "a2", Method: "GET", URL: "https://example.com", Auth: &HTTPAuth{Type: "api_key", Key: "k"}},
		{RequestID: "b", Method: "POST", URL: "https://example.com", Body: &HTTPBody{Type: "json", Content: "{oops"}},
		{RequestID: "s", Method: "GET", URL: "https://example.com", ExpectedStatus: "2xx-3xx"},
		{RequestID: "as", Method: "GET", URL: "https://example.com", Assertions: []HTTPAssertion{{Type: "regex", Value: "x"}}},
		{RequestID: "t", Method: "GET", URL: "https://example.com", TimeoutSeconds: 601},
		{RequestID: "r", Method: "GET", URL: "https://example.com", RetryCount: 11},
		{RequestID: "c", Method: "GET", URL: "https://example.com", CronSchedule: "every day"},
		{RequestID: "l", Method: "GET", URL: "https://example.com", LogStorage: "tape"},
		{RequestID: "h", Method: "GET", URL: "https://example.com", Headers: []HTTPKeyValue{{Key: "", Value: "x"}}},
	}
	for _, req := range bad {
		if _, err := validateHTTPRequests([]HTTPRequest{req}, map[string]bool{"cmd-a": true}); err == nil {
			t.Errorf("expected request %q to be rejected", req.RequestID)
		}
	}
	if _, err := validateHTTPRequests([]HTTPRequest{good, good}, nil); err == nil {
		t.Error("duplicate request IDs must be rejected")
	}
}

func TestHTTPRequestCRUD(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	target := newHTTPTargetServer(t)

	t.Run("Create", func(t *testing.T) {
		def := httpRequestDefinition("crud-one", "POST", target.URL+"/echo")
		def["auth"] = map[string]interface{}{"type": "bearer", "token": "secret-token"}
		def["headers"] = []map[string]interface{}{{"key": "X-Api-Key", "value": "hidden"}, {"key": "X-Custom", "value": "visible"}}
		def["bearer_token"] = "trigger-token"
		def["cron_schedule"] = "*/5 * * * *"
		resp := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", def)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
		body := AssertJSONResponse(t, resp)
		AssertSuccess(t, body)
		data := dataMap(t, body)
		auth := data["auth"].(map[string]interface{})
		if _, leaked := auth["token"]; leaked {
			t.Error("auth token must not be returned")
		}
		if auth["has_secret"] != true {
			t.Error("expected has_secret flag")
		}
		headers := data["headers"].([]interface{})
		first := headers[0].(map[string]interface{})
		if first["value"] != httpMaskedSecret || first["masked"] != true {
			t.Errorf("secret header should be masked, got %v", first)
		}
		if headers[1].(map[string]interface{})["value"] != "visible" {
			t.Error("non-secret header value must be preserved")
		}
		if data["bearer_token"] != "trigger-token" {
			t.Error("admin view must include the trigger bearer token")
		}
	})

	t.Run("ListAndStatus", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "GET", "/api/config/http-requests", nil)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		body := AssertJSONResponse(t, resp)
		items := body["data"].([]interface{})
		if len(items) != 1 {
			t.Fatalf("expected 1 request, got %d", len(items))
		}
		cfg := ts.GetConfig(t)
		if _, ok := cfg["http_requests"]; !ok {
			t.Fatal("http_requests array missing from saved config.json")
		}
	})

	t.Run("DuplicateCommandID", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", httpRequestDefinition("test-echo", "GET", target.URL+"/json"))
		defer resp.Body.Close()
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("InvalidDefinition", func(t *testing.T) {
		resp := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", httpRequestDefinition("bad-method", "FETCH", target.URL+"/json"))
		defer resp.Body.Close()
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("VariablesInDefinition", func(t *testing.T) {
		// test_config.json defines variables.test_var = "test_value"; raw values must be saved unsubstituted.
		def := httpRequestDefinition("crud-var", "GET", target.URL+"/json?tag={{test_var}}")
		resp := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", def)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
		stored := ts.GetConfig(t)["http_requests"].([]interface{})
		if !strings.Contains(stored[len(stored)-1].(map[string]interface{})["url"].(string), "{{test_var}}") {
			t.Error("raw config must keep the {{test_var}} placeholder")
		}
		exec := ts.DoAuthRequest(t, "POST", "/api/http-requests/crud-var", nil)
		out := dataMap(t, AssertJSONResponse(t, exec))
		exec.Body.Close()
		if !strings.HasSuffix(out["url"].(string), "tag=test_value") {
			t.Errorf("variable was not substituted at run time: %v", out["url"])
		}
		bad := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", httpRequestDefinition("crud-var-bad", "GET", "https://{{no_such_var}}.example.com/"))
		bad.Body.Close()
		AssertStatus(t, http.StatusBadRequest, bad.StatusCode)
		del := ts.DoAuthRequest(t, "DELETE", "/api/config/http-requests/crud-var", nil)
		del.Body.Close()
		AssertStatus(t, http.StatusOK, del.StatusCode)
	})

	t.Run("UpdatePreservesSecrets", func(t *testing.T) {
		def := httpRequestDefinition("crud-one", "POST", target.URL+"/echo")
		def["auth"] = map[string]interface{}{"type": "bearer", "token": ""}
		def["headers"] = []map[string]interface{}{{"key": "X-Api-Key", "value": httpMaskedSecret}, {"key": "X-Custom", "value": "changed"}}
		def["bearer_token"] = "trigger-token"
		resp := ts.DoAuthRequest(t, "PUT", "/api/config/http-requests/crud-one", def)
		defer resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)

		cfg := ts.GetConfig(t)
		stored := cfg["http_requests"].([]interface{})[0].(map[string]interface{})
		if stored["auth"].(map[string]interface{})["token"] != "secret-token" {
			t.Errorf("auth token was not preserved: %v", stored["auth"])
		}
		headers := stored["headers"].([]interface{})
		if headers[0].(map[string]interface{})["value"] != "hidden" {
			t.Errorf("masked header value was not preserved: %v", headers[0])
		}
		if headers[1].(map[string]interface{})["value"] != "changed" {
			t.Errorf("plain header value was not updated: %v", headers[1])
		}

		// Execute to prove the preserved secrets reach the target.
		exec := ts.DoAuthRequest(t, "POST", "/api/http-requests/crud-one", nil)
		defer exec.Body.Close()
		AssertStatus(t, http.StatusOK, exec.StatusCode)
		out := dataMap(t, AssertJSONResponse(t, exec))
		if !strings.Contains(out["output"].(string), `"authorization":"Bearer secret-token"`) || !strings.Contains(out["output"].(string), `"x_api_key":"hidden"`) {
			t.Errorf("target did not receive preserved secrets: %v", out["output"])
		}
	})

	t.Run("DeleteGuards", func(t *testing.T) {
		user := map[string]interface{}{"username": "httpviewer", "temporary_password": "temporary-password-123", "http_request_ids": []string{"crud-one"}}
		created := ts.DoAuthRequest(t, "POST", "/api/users", user)
		created.Body.Close()
		AssertStatus(t, http.StatusCreated, created.StatusCode)

		resp := ts.DoAuthRequest(t, "DELETE", "/api/config/http-requests/crud-one", nil)
		resp.Body.Close()
		AssertStatus(t, http.StatusConflict, resp.StatusCode)

		removed := ts.DoAuthRequest(t, "DELETE", "/api/users/httpviewer", nil)
		removed.Body.Close()
		AssertStatus(t, http.StatusOK, removed.StatusCode)

		runBook := map[string]interface{}{"runbook_id": "http-guard-book", "steps": []map[string]interface{}{{"step_id": "s1", "request_id": "crud-one"}}}
		rb := ts.DoAuthRequest(t, "POST", "/api/config/runbooks", runBook)
		rb.Body.Close()
		AssertStatus(t, http.StatusCreated, rb.StatusCode)
		resp = ts.DoAuthRequest(t, "DELETE", "/api/config/http-requests/crud-one", nil)
		resp.Body.Close()
		AssertStatus(t, http.StatusConflict, resp.StatusCode)
		rbDel := ts.DoAuthRequest(t, "DELETE", "/api/config/runbooks/http-guard-book", nil)
		rbDel.Body.Close()
		AssertStatus(t, http.StatusOK, rbDel.StatusCode)

		resp = ts.DoAuthRequest(t, "DELETE", "/api/config/http-requests/crud-one", nil)
		resp.Body.Close()
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		missing := ts.DoAuthRequest(t, "DELETE", "/api/config/http-requests/crud-one", nil)
		missing.Body.Close()
		AssertStatus(t, http.StatusNotFound, missing.StatusCode)
	})

	t.Run("UnknownUserGrantRejected", func(t *testing.T) {
		user := map[string]interface{}{"username": "ghost", "temporary_password": "temporary-password-123", "http_request_ids": []string{"does-not-exist"}}
		resp := ts.DoAuthRequest(t, "POST", "/api/users", user)
		resp.Body.Close()
		AssertStatus(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestHTTPRequestExecute(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	target := newHTTPTargetServer(t)

	create := func(t *testing.T, def map[string]interface{}) {
		t.Helper()
		resp := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", def)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %v failed: %v", def["request_id"], AssertJSONResponse(t, resp))
		}
	}
	run := func(t *testing.T, id string, headers map[string]string) (*http.Response, map[string]interface{}) {
		t.Helper()
		resp := ts.DoRequest(t, "GET", "/api/http-requests/"+id, nil, headers)
		body := AssertJSONResponse(t, resp)
		resp.Body.Close()
		return resp, body
	}
	jwt := map[string]string{"Authorization": "Bearer " + ts.AdminToken}

	t.Run("GetJSONWithAssertions", func(t *testing.T) {
		def := httpRequestDefinition("exec-json", "GET", target.URL+"/json")
		def["query_params"] = []map[string]interface{}{{"key": "verbose", "value": "1"}}
		def["assertions"] = []map[string]interface{}{
			{"type": "body_contains", "value": `"status":"ok"`},
			{"type": "json_path_equals", "path": "data.items.0.id", "value": "7"},
			{"type": "max_response_ms", "value": "5000"},
		}
		create(t, def)
		resp, body := run(t, "exec-json", jwt)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		AssertSuccess(t, body)
		out := dataMap(t, body)
		if out["exit_code"].(float64) != 0 || out["http_status"].(float64) != 200 {
			t.Fatalf("unexpected outcome: %v", out)
		}
		if len(out["checks"].([]interface{})) != 4 {
			t.Errorf("expected 4 checks, got %v", out["checks"])
		}
		headers := out["response_headers"].(map[string]interface{})
		if _, ok := headers["X-Target"]; !ok {
			t.Errorf("response headers missing: %v", headers)
		}
		if out["trace_id"] == "" || out["attempts"].(float64) != 1 {
			t.Errorf("trace/attempt data wrong: %v", out)
		}
	})

	t.Run("PostFormBodyAndApiKeyQuery", func(t *testing.T) {
		def := httpRequestDefinition("exec-form", "POST", target.URL+"/echo")
		def["body"] = map[string]interface{}{"type": "form", "fields": []map[string]interface{}{{"key": "a", "value": "1"}, {"key": "b", "value": "two words"}}}
		def["auth"] = map[string]interface{}{"type": "api_key", "key": "api_key", "value": "qsecret", "add_to": "query"}
		def["assertions"] = []map[string]interface{}{{"type": "json_path_equals", "path": "body", "value": "a=1&b=two+words"}, {"type": "json_path_equals", "path": "query.api_key.0", "value": "qsecret"}}
		create(t, def)
		resp, body := run(t, "exec-form", jwt)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		out := dataMap(t, body)
		if out["exit_code"].(float64) != 0 {
			t.Fatalf("form request failed: %v", out)
		}
		if !strings.Contains(out["output"].(string), `"content_type":"application/x-www-form-urlencoded"`) {
			t.Errorf("content type not set: %v", out["output"])
		}
	})

	t.Run("BearerTokenAuthorisation", func(t *testing.T) {
		def := httpRequestDefinition("exec-token", "GET", target.URL+"/json")
		def["bearer_token"] = "ext-token"
		create(t, def)
		resp, _ := run(t, "exec-token", map[string]string{"Authorization": "Bearer ext-token"})
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		resp, _ = run(t, "exec-token", map[string]string{"Authorization": "Bearer wrong"})
		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
		resp, _ = run(t, "exec-token", nil)
		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
		resp, _ = run(t, "exec-json", nil) // no bearer_token configured: API access denied
		AssertStatus(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("UnexpectedStatusIsFailure", func(t *testing.T) {
		def := httpRequestDefinition("exec-fail", "GET", target.URL+"/fail")
		def["retry_count"] = 2
		create(t, def)
		resp, body := run(t, "exec-fail", jwt)
		AssertStatus(t, http.StatusOK, resp.StatusCode)
		out := dataMap(t, body)
		if out["exit_code"].(float64) != 1 || out["http_status"].(float64) != 500 {
			t.Fatalf("expected failed run with HTTP 500: %v", out)
		}
		if out["attempts"].(float64) != 3 {
			t.Errorf("expected 3 attempts on 5xx, got %v", out["attempts"])
		}
		if !strings.Contains(out["error"].(string), "HTTP 500 not in expected 2xx") {
			t.Errorf("error text: %v", out["error"])
		}
	})

	t.Run("ExpectedStatusAccepts500", func(t *testing.T) {
		def := httpRequestDefinition("exec-accept-500", "GET", target.URL+"/fail")
		def["expected_status"] = "500"
		create(t, def)
		_, body := run(t, "exec-accept-500", jwt)
		if dataMap(t, body)["exit_code"].(float64) != 0 {
			t.Errorf("500 should satisfy expected_status 500: %v", body)
		}
	})

	t.Run("AssertionFailure", func(t *testing.T) {
		def := httpRequestDefinition("exec-assert", "GET", target.URL+"/json")
		def["assertions"] = []map[string]interface{}{{"type": "body_contains", "value": "missing-text"}}
		create(t, def)
		_, body := run(t, "exec-assert", jwt)
		out := dataMap(t, body)
		if out["exit_code"].(float64) != 1 || !strings.Contains(out["error"].(string), "missing-text") {
			t.Errorf("assertion failure not reported: %v", out)
		}
	})

	t.Run("TimeoutIsTransportFailure", func(t *testing.T) {
		def := httpRequestDefinition("exec-slow", "GET", target.URL+"/slow")
		def["timeout_seconds"] = 1
		create(t, def)
		resp, body := run(t, "exec-slow", jwt)
		AssertStatus(t, http.StatusInternalServerError, resp.StatusCode)
		AssertError(t, body)
		out := dataMap(t, body)
		if out["http_status"].(float64) != 0 || out["trace_id"] == "" {
			t.Errorf("transport failure payload wrong: %v", out)
		}
		if !strings.Contains(out["error"].(string), "Timed out: no complete response within 1 seconds") || strings.Contains(out["error"].(string), "context deadline") {
			t.Errorf("timeout must be explained in plain language, got: %v", out["error"])
		}
	})

	t.Run("RedirectsOff", func(t *testing.T) {
		def := httpRequestDefinition("exec-redirect", "GET", target.URL+"/redirect")
		def["follow_redirects"] = false
		def["expected_status"] = "3xx"
		create(t, def)
		_, body := run(t, "exec-redirect", jwt)
		out := dataMap(t, body)
		if out["http_status"].(float64) != 302 || out["exit_code"].(float64) != 0 {
			t.Errorf("redirect not reported as 302: %v", out)
		}
		def = httpRequestDefinition("exec-redirect-on", "GET", target.URL+"/redirect")
		create(t, def)
		_, body = run(t, "exec-redirect-on", jwt)
		if dataMap(t, body)["http_status"].(float64) != 200 {
			t.Errorf("redirect was not followed: %v", body)
		}
	})

	t.Run("OutputTruncation", func(t *testing.T) {
		def := httpRequestDefinition("exec-big", "GET", target.URL+"/big")
		def["max_output_size"] = "10B"
		create(t, def)
		_, body := run(t, "exec-big", jwt)
		out := dataMap(t, body)
		if out["is_output_truncated"].(float64) != 1 || out["response_bytes"].(float64) != 500 {
			t.Errorf("truncation not reported: %v", out)
		}
		logResp := ts.DoAuthRequest(t, "GET", "/api/logs/"+out["trace_id"].(string), nil)
		logBody, _ := io.ReadAll(logResp.Body)
		logResp.Body.Close()
		if strings.Count(string(logBody), "0123456789") != 50 {
			t.Error("log file must hold the full untruncated body")
		}
	})

	t.Run("UnknownRequest", func(t *testing.T) {
		resp, _ := run(t, "nope", jwt)
		AssertStatus(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestHTTPRequestLogFile(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	target := newHTTPTargetServer(t)

	def := httpRequestDefinition("log-one", "POST", target.URL+"/echo")
	def["auth"] = map[string]interface{}{"type": "basic", "username": "u", "password": "p"}
	def["body"] = map[string]interface{}{"type": "json", "content": `{"hello":"world"}`}
	created := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", def)
	created.Body.Close()
	AssertStatus(t, http.StatusCreated, created.StatusCode)

	exec := ts.DoAuthRequest(t, "POST", "/api/http-requests/log-one", nil)
	out := dataMap(t, AssertJSONResponse(t, exec))
	exec.Body.Close()
	traceID := out["trace_id"].(string)

	matches, _ := filepath.Glob("logs/*_log-one_" + traceID + "_log.txt")
	if len(matches) != 1 {
		t.Fatalf("expected one log file for trace %s, found %v", traceID, matches)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"job_type : http_request", "request_id : log-one", "method : POST", "http_status : 200", "attempts : 1", "exitcode : 0",
		"--- request (attempt 1) ---", "POST /echo HTTP/1.1", "Authorization: Basic ****", "Content-Type: application/json", `{"hello":"world"}`,
		"--- response headers (attempt 1) ---", "HTTP/1.1 200 OK", "--- response body (attempt 1) ---", `"authorization":"Basic`, "--- checks ---", "PASS status",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log file missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "Authorization: Basic dTpw") {
		t.Error("basic credentials leaked into the request dump")
	}
	fields := parseLogKeyValueFields(text)
	if fields["job_type"] != httpRequestJobType || fields["request_id"] != "log-one" || fields["response_ms"] == "" {
		t.Errorf("header fields not parsed: %v", fields)
	}

	list := ts.DoAuthRequest(t, "GET", "/api/logs?limit=20", nil)
	items := AssertJSONResponse(t, list)["data"].([]interface{})
	list.Body.Close()
	found := false
	for _, item := range items {
		entry := item.(map[string]interface{})
		if entry["trace_id"] == traceID {
			found = true
			if entry["job_type"] != httpRequestJobType || entry["request_id"] != "log-one" || entry["status"] != "Success" {
				t.Errorf("log listing entry wrong: %v", entry)
			}
		}
	}
	if !found {
		t.Error("HTTP request log not listed by /api/logs")
	}

	contentResp := ts.DoAuthRequest(t, "GET", "/api/logs/"+traceID+"/content", nil)
	contentData := dataMap(t, AssertJSONResponse(t, contentResp))
	contentResp.Body.Close()
	if contentData["job_type"] != httpRequestJobType || contentData["cmd_id"] != "log-one" {
		t.Errorf("log content metadata wrong: %v", contentData)
	}

	// Grant-based access for regular users.
	original := authManager
	defer func() { authManager = original }()
	authManager = &AuthManager{users: map[string]User{"viewer": normalizeUser(User{Username: "viewer", Enabled: true, HTTPRequestIDs: []string{"log-one"}, CommandIDs: []string{"log-one"}})}}
	request := withClaims(httptest.NewRequest("GET", "/api/logs", nil), &Claims{Username: "viewer", Role: RoleUser})
	if !canAccessLogFile(request, matches[0]) {
		t.Error("HTTP request grant did not expose the log")
	}
	authManager = &AuthManager{users: map[string]User{"viewer": normalizeUser(User{Username: "viewer", Enabled: true, CommandIDs: []string{"log-one"}})}}
	if canAccessLogFile(request, matches[0]) {
		t.Error("a Command grant must not expose an HTTP request log")
	}
	_ = os.Remove(matches[0])
}

func TestHTTPRequestInRunBook(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()
	target := newHTTPTargetServer(t)

	for _, def := range []map[string]interface{}{
		httpRequestDefinition("rb-ok", "GET", target.URL+"/json"),
		httpRequestDefinition("rb-fail", "GET", target.URL+"/fail"),
	} {
		resp := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", def)
		resp.Body.Close()
		AssertStatus(t, http.StatusCreated, resp.StatusCode)
	}

	invalid := map[string]interface{}{"runbook_id": "http-book-bad", "steps": []map[string]interface{}{{"step_id": "s1", "cmd_id": "test-echo", "request_id": "rb-ok"}}}
	resp := ts.DoAuthRequest(t, "POST", "/api/config/runbooks", invalid)
	resp.Body.Close()
	AssertStatus(t, http.StatusBadRequest, resp.StatusCode)

	runBook := map[string]interface{}{"runbook_id": "http-book", "steps": []map[string]interface{}{
		{"step_id": "first", "request_id": "rb-ok"},
		{"step_id": "second", "request_id": "rb-fail", "on_failure": "continue", "retry_count": 1},
		{"step_id": "third", "request_id": "rb-ok", "label": "again"},
	}}
	resp = ts.DoAuthRequest(t, "POST", "/api/config/runbooks", runBook)
	resp.Body.Close()
	AssertStatus(t, http.StatusCreated, resp.StatusCode)

	start := ts.DoAuthRequest(t, "POST", "/api/runbooks/http-book/runs", nil)
	startBody := AssertJSONResponse(t, start)
	start.Body.Close()
	AssertStatus(t, http.StatusAccepted, start.StatusCode)
	runID := dataMap(t, startBody)["run_id"].(string)

	var run map[string]interface{}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		poll := ts.DoAuthRequest(t, "GET", "/api/runbooks/http-book/runs/"+runID, nil)
		run = dataMap(t, AssertJSONResponse(t, poll))
		poll.Body.Close()
		status := run["status"].(string)
		if status != "queued" && status != "running" && status != "retrying" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if run["status"] != "failed" || run["success_count"].(float64) != 2 || run["failure_count"].(float64) != 1 {
		t.Fatalf("unexpected run outcome: %v", run)
	}
	steps := run["steps"].([]interface{})
	second := steps[1].(map[string]interface{})
	if second["request_id"] != "rb-fail" || second["status"] != "failed" || len(second["attempts"].([]interface{})) != 2 {
		t.Errorf("second step wrong: %v", second)
	}
	first := steps[0].(map[string]interface{})
	if first["status"] != "succeeded" || !strings.Contains(first["output"].(string), `"status":"ok"`) {
		t.Errorf("first step wrong: %v", first)
	}
	traceID := first["trace_id"].(string)
	matches, _ := filepath.Glob("logs/*_rb-ok_" + traceID + "_log.txt")
	if len(matches) != 1 {
		t.Fatalf("step log missing for trace %s", traceID)
	}
	content, _ := os.ReadFile(matches[0])
	for _, want := range []string{"runbook_id : http-book", "run_id : " + runID, "step_id : first", "trigger : runbook", "job_type : http_request"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("step log missing %q", want)
		}
	}

	del := ts.DoAuthRequest(t, "DELETE", "/api/config/http-requests/rb-ok", nil)
	del.Body.Close()
	AssertStatus(t, http.StatusConflict, del.StatusCode)

	// A step time limit must be explained in plain language, not as "context deadline exceeded".
	slow := ts.DoAuthRequest(t, "POST", "/api/config/http-requests", httpRequestDefinition("rb-slow", "GET", target.URL+"/slow"))
	slow.Body.Close()
	AssertStatus(t, http.StatusCreated, slow.StatusCode)
	slowBook := map[string]interface{}{"runbook_id": "http-book-slow", "steps": []map[string]interface{}{{"step_id": "s1", "request_id": "rb-slow", "timeout_seconds": 1}}}
	created := ts.DoAuthRequest(t, "POST", "/api/config/runbooks", slowBook)
	created.Body.Close()
	AssertStatus(t, http.StatusCreated, created.StatusCode)
	start = ts.DoAuthRequest(t, "POST", "/api/runbooks/http-book-slow/runs", nil)
	runID = dataMap(t, AssertJSONResponse(t, start))["run_id"].(string)
	start.Body.Close()
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		poll := ts.DoAuthRequest(t, "GET", "/api/runbooks/http-book-slow/runs/"+runID, nil)
		run = dataMap(t, AssertJSONResponse(t, poll))
		poll.Body.Close()
		if status := run["status"].(string); status != "queued" && status != "running" && status != "retrying" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	slowStep := run["steps"].([]interface{})[0].(map[string]interface{})
	if slowStep["status"] != "timed_out" || !strings.Contains(slowStep["error"].(string), "reached its time limit of 1 seconds") || strings.Contains(slowStep["error"].(string), "context deadline") {
		t.Errorf("step timeout not explained in plain language: %v", slowStep)
	}
}

func TestDescribeTimeout(t *testing.T) {
	step := describeTimeout("step", 4000, 4000*time.Second+5*time.Millisecond)
	if !strings.Contains(step, "stopped after 1h6m40s") || !strings.Contains(step, "time limit of 4000 seconds (1h6m40s)") || !strings.Contains(step, "RunBook editor") {
		t.Errorf("step wording: %s", step)
	}
	book := describeTimeout("runbook", 600, 723*time.Second)
	if !strings.Contains(book, "stopped after 12m3s") || !strings.Contains(book, "overall time limit of 600 seconds (10m0s)") {
		t.Errorf("runbook wording: %s", book)
	}
	if http := describeTimeout("http", 30, 30*time.Second); !strings.Contains(http, "no complete response within 30 seconds (30s)") {
		t.Errorf("http wording: %s", http)
	}
	limits := runBookStepLimits{step: RunBookStep{TimeoutSeconds: 5}}
	if msg := limits.timeoutMessage(6 * time.Second); !strings.Contains(msg, "time limit of 5 seconds") {
		t.Errorf("limits wording: %s", msg)
	}
}

func TestHTTPRequestCronAndStatus(t *testing.T) {
	ts := NewTestServer(t)
	defer ts.Close()

	cfg := ts.GetConfig(t)
	cfg["cron_enabled"] = "Y"
	cfg["http_requests"] = []map[string]interface{}{{"request_id": "cron-ping", "method": "GET", "url": "https://example.invalid/ping", "cron_schedule": "0 3 * * *"}}
	ts.UpdateConfig(t, cfg)

	scheduledJobsMutex.RLock()
	_, registered := scheduledJobs["http:cron-ping"]
	scheduledJobsMutex.RUnlock()
	if !registered {
		t.Fatal("HTTP request cron job was not registered")
	}

	resp := ts.DoAuthRequest(t, "GET", "/api/status", nil)
	status := dataMap(t, AssertJSONResponse(t, resp))
	resp.Body.Close()
	scheduled := status["scheduled_http_requests"].([]interface{})
	if len(scheduled) != 1 || scheduled[0].(map[string]interface{})["cmd_id"] != "cron-ping" || scheduled[0].(map[string]interface{})["cron_schedule"] != "0 3 * * *" {
		t.Errorf("scheduled_http_requests wrong: %v", scheduled)
	}
	if len(status["scheduled_jobs"].([]interface{})) != 0 {
		t.Errorf("HTTP request must not appear under scheduled_jobs: %v", status["scheduled_jobs"])
	}

	user := ts.DoAuthRequest(t, "GET", "/api/user/http-requests", nil)
	items := AssertJSONResponse(t, user)["data"].([]interface{})
	user.Body.Close()
	if len(items) != 1 || items[0].(map[string]interface{})["request_id"] != "cron-ping" {
		t.Errorf("assigned HTTP requests wrong: %v", items)
	}

	sched := ts.DoAuthRequest(t, "PUT", "/api/user/http-requests/cron-ping/schedule", map[string]string{"cron_schedule": "*/15 * * * *"})
	sched.Body.Close()
	AssertStatus(t, http.StatusOK, sched.StatusCode)
	saved := ts.GetConfig(t)["http_requests"].([]interface{})[0].(map[string]interface{})
	if saved["cron_schedule"] != "*/15 * * * *" {
		t.Errorf("schedule not saved: %v", saved)
	}

	overview := ts.DoAuthRequest(t, "GET", "/api/overview/scheduled-jobs", nil)
	jobs := dataMap(t, AssertJSONResponse(t, overview))["jobs"].([]interface{})
	overview.Body.Close()
	foundType := ""
	for _, job := range jobs {
		entry := job.(map[string]interface{})
		if entry["job_id"] == "cron-ping" {
			foundType, _ = entry["type"].(string)
		}
	}
	if foundType != httpRequestJobType {
		t.Errorf("overview did not list the HTTP request with type http_request: %v", jobs)
	}
}
