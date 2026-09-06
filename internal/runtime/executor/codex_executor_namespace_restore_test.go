package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorRestoresUniqueShortWaitAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_wait","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_wait","name":"wait_agent","namespace":null,"arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[{
			"type":"namespace",
			"name":"multi_agent_v1",
			"tools":[
				{"type":"function","name":"spawn_agent"},
				{"type":"function","name":"wait_agent"}
			]
		}],
		"input":[{"type":"message","role":"user","content":"wait"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	item := gjson.GetBytes(resp, "output.0")
	if item.Get("namespace").String() != "multi_agent_v1" || item.Get("name").String() != "wait_agent" {
		t.Fatalf("unique short wait_agent not restored: %s", resp)
	}
}

func TestCodexExecutorRestoresV1DottedNamespaceFromInventory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_v1","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_v1","name":"multi_agent_v1.spawn_agent","namespace":null,"arguments":"{\"message\":\"go\"}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[{
			"type":"namespace",
			"name":"multi_agent_v1",
			"tools":[
				{"type":"function","name":"spawn_agent"},
				{"type":"function","name":"wait_agent"}
			]
		}],
		"input":[{"type":"message","role":"user","content":"spawn"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	item := gjson.GetBytes(resp, "output.0")
	if item.Get("namespace").String() != "multi_agent_v1" || item.Get("name").String() != "spawn_agent" {
		t.Fatalf("v1 dotted call not restored: %s", resp)
	}
	if item.Get("call_id").String() != "call_v1" {
		t.Fatalf("call_id changed: %s", resp)
	}
	if item.Get("arguments").String() != `{"message":"go"}` {
		t.Fatalf("arguments changed: %s", resp)
	}
}

func TestCodexExecutorRestoresOptimizedV2DottedAliasFromRequest(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upstreamBody, _ = io.ReadAll(request.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_v2","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_v2","name":"collaboration-optimize.spawn_agent","namespace":null,"arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	resp := executeCodexNamespaceRestore(t, server.URL, codexSpawnAgentTestPayload(), true)
	if gjson.GetBytes(upstreamBody, "input.0.tools.0.name").String() != "collaboration-optimize" {
		t.Fatalf("optimizer alias was not recorded on the request: %s", upstreamBody)
	}
	item := gjson.GetBytes(resp, "output.0")
	if item.Get("namespace").String() != "collaboration" || item.Get("name").String() != "spawn_agent" {
		t.Fatalf("optimized dotted alias not restored to canonical: %s", resp)
	}
	if bytes.Contains(resp, []byte("collaboration-optimize")) {
		t.Fatalf("alias leaked to client: %s", resp)
	}
}

func TestCodexExecutorLeavesLegitimateDottedFunctionUntouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_fn","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_fn","name":"custom.report","namespace":null,"arguments":"{\"x\":1}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[
			{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"}]},
			{"type":"function","name":"custom.report"}
		],
		"input":[{"type":"message","role":"user","content":"report"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	item := gjson.GetBytes(resp, "output.0")
	if item.Get("name").String() != "custom.report" {
		t.Fatalf("legitimate dotted function rewritten: %s", resp)
	}
	if item.Get("namespace").String() == "multi_agent_v1" {
		t.Fatalf("legitimate dotted function routed into namespace: %s", resp)
	}
}

func TestCodexExecutorCollisionFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_c","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_c","name":"collaboration.spawn_agent","namespace":null,"arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]},
			{"type":"function","name":"collaboration.spawn_agent"}
		],
		"input":[{"type":"message","role":"user","content":"ambiguous"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	item := gjson.GetBytes(resp, "output.0")
	if item.Get("name").String() != "collaboration.spawn_agent" {
		t.Fatalf("collision was silently restored: %s", resp)
	}
	if item.Get("namespace").String() == "collaboration" && item.Get("name").String() == "spawn_agent" {
		t.Fatalf("collision routed to collaboration: %s", resp)
	}
}

func TestCodexExecutorStreamRestoresDottedNamespaceEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item_1","type":"function_call","call_id":"call_s","name":"multi_agent_v1.wait_agent","namespace":null,"arguments":"{}"}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"item_1","type":"function_call","call_id":"call_s","name":"multi_agent_v1.wait_agent","namespace":null,"arguments":"{}"}}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_s","object":"response","status":"completed","output":[{"id":"item_1","type":"function_call","call_id":"call_s","name":"multi_agent_v1.wait_agent","namespace":null,"arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[{
			"type":"namespace",
			"name":"multi_agent_v1",
			"tools":[
				{"type":"function","name":"spawn_agent"},
				{"type":"function","name":"wait_agent"}
			]
		}],
		"input":[{"type":"message","role":"user","content":"wait"}]
	}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var buf []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		buf = append(buf, chunk.Payload...)
	}
	if !bytes.Contains(buf, []byte(`"namespace":"multi_agent_v1"`)) {
		t.Fatalf("streaming restore missing canonical namespace: %s", buf)
	}
	if bytes.Contains(buf, []byte("multi_agent_v1.wait_agent")) {
		t.Fatalf("streaming dotted name leaked: %s", buf)
	}
	if !bytes.Contains(buf, []byte(`"name":"wait_agent"`)) {
		t.Fatalf("streaming child name missing: %s", buf)
	}
}

func executeCodexNamespaceRestore(t *testing.T, baseURL string, payload []byte, optimize bool) []byte {
	t.Helper()
	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: optimize}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": baseURL, "api_key": "test"}}
	ctx := context.Background()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
	if optimize {
		ctx = codexSpawnAgentTestContext()
		opts.Headers = http.Header{"User-Agent": []string{"overridden-client/1.0"}}
	}
	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: payload}, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("empty client payload")
	}
	return resp.Payload
}

func TestCodexExecutorUnknownDottedNameUntouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_u","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_u","name":"unknown.tool","namespace":null,"arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[{"type":"message","role":"user","content":"x"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	if gjson.GetBytes(resp, "output.0.name").String() != "unknown.tool" {
		t.Fatalf("unknown dotted name rewritten: %s", resp)
	}
}

func TestCodexExecutorMalformedUpstreamJSONPassThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {not-json\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_m","object":"response","status":"completed","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"gpt-5.4",
		"tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[{"type":"message","role":"user","content":"x"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	if gjson.GetBytes(resp, "id").String() != "resp_m" && gjson.GetBytes(resp, "response.id").String() != "resp_m" {
		t.Fatalf("malformed event caused execute failure or lost completion: %s", resp)
	}
}

func TestCodexExecutorDoesNotUseModelNameBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(fmt.Sprintf(
			`data: {"type":"response.completed","response":{"id":"resp_x","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_x","name":"custom_ns.do_work","namespace":null,"arguments":"{}"}]}}` + "\n\n",
		)))
	}))
	defer server.Close()

	payload := []byte(`{
		"model":"not-a-special-model",
		"tools":[{"type":"namespace","name":"custom_ns","tools":[{"type":"function","name":"do_work"}]}],
		"input":[{"type":"message","role":"user","content":"x"}]
	}`)
	resp := executeCodexNamespaceRestore(t, server.URL, payload, false)
	item := gjson.GetBytes(resp, "output.0")
	if item.Get("namespace").String() != "custom_ns" || item.Get("name").String() != "do_work" {
		t.Fatalf("inventory-driven restore failed without model-specific logic: %s", resp)
	}
}
