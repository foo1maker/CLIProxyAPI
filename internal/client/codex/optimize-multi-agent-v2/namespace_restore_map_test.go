package multiagentv2

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func v1Inventory() []byte {
	return []byte(`{
		"tools":[{
			"type":"namespace",
			"name":"multi_agent_v1",
			"tools":[
				{"type":"function","name":"spawn_agent"},
				{"type":"function","name":"wait_agent"},
				{"type":"function","name":"send_input"}
			]
		}]
	}`)
}

func v2Inventory() []byte {
	return []byte(`{
		"tools":[{
			"type":"namespace",
			"name":"collaboration",
			"tools":[
				{"type":"function","name":"spawn_agent"},
				{"type":"function","name":"wait_agent"},
				{"type":"function","name":"send_message"},
				{"type":"function","name":"list_agents"}
			]
		}]
	}`)
}

func optimizedV2Inventory() []byte {
	return []byte(`{
		"tools":[{
			"type":"namespace",
			"name":"collaboration-optimize",
			"tools":[
				{"type":"function","name":"spawn_agent"},
				{"type":"function","name":"wait_agent"},
				{"type":"function","name":"send_message"},
				{"type":"function","name":"list_agents"}
			]
		}]
	}`)
}

func restoreJSON(t *testing.T, original, optimized, payload []byte) []byte {
	t.Helper()
	got := RestoreCodexNamespaceToolsFromMap(payload, BuildCodexNamespaceRestoreMap(original, optimized))
	if !gjson.ValidBytes(got) {
		t.Fatalf("restored payload is not valid JSON: %s", got)
	}
	return got
}

func TestRestoreV1DottedNamespaceCall(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"response":{"output":[{
			"type":"function_call",
			"call_id":"call_v1_1",
			"name":"multi_agent_v1.spawn_agent",
			"namespace":null,
			"arguments":"{\"message\":\"read marker\"}"
		}]}
	}`)
	got := restoreJSON(t, v1Inventory(), v1Inventory(), payload)
	item := gjson.GetBytes(got, "response.output.0")
	if item.Get("namespace").String() != "multi_agent_v1" {
		t.Fatalf("namespace = %q", item.Get("namespace").String())
	}
	if item.Get("name").String() != "spawn_agent" {
		t.Fatalf("name = %q", item.Get("name").String())
	}
	if item.Get("call_id").String() != "call_v1_1" {
		t.Fatalf("call_id changed: %q", item.Get("call_id").String())
	}
	if item.Get("arguments").String() != `{"message":"read marker"}` {
		t.Fatalf("arguments changed: %q", item.Get("arguments").String())
	}
}

func TestRestoreV2DottedNamespaceCall(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"response":{"output":[{
			"type":"function_call",
			"call_id":"call_v2_1",
			"name":"collaboration.spawn_agent",
			"namespace":null,
			"arguments":"{}"
		}]}
	}`)
	got := restoreJSON(t, v2Inventory(), v2Inventory(), payload)
	item := gjson.GetBytes(got, "response.output.0")
	if item.Get("namespace").String() != "collaboration" || item.Get("name").String() != "spawn_agent" {
		t.Fatalf("got namespace=%q name=%q", item.Get("namespace").String(), item.Get("name").String())
	}
	if item.Get("call_id").String() != "call_v2_1" {
		t.Fatalf("call_id changed")
	}
}

func TestRestoreOptimizedV2AliasFromRequestRename(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"response":{"output":[{
			"type":"function_call",
			"call_id":"call_opt_1",
			"name":"collaboration-optimize.spawn_agent",
			"namespace":null,
			"arguments":"{\"task_name\":\"t\"}"
		}]}
	}`)
	got := restoreJSON(t, v2Inventory(), optimizedV2Inventory(), payload)
	item := gjson.GetBytes(got, "response.output.0")
	if item.Get("namespace").String() != "collaboration" {
		t.Fatalf("namespace = %q, want collaboration (canonical, not alias)", item.Get("namespace").String())
	}
	if item.Get("name").String() != "spawn_agent" {
		t.Fatalf("name = %q", item.Get("name").String())
	}
	if item.Get("arguments").String() != `{"task_name":"t"}` {
		t.Fatalf("arguments changed")
	}

	// Near-prefix names are not restored: the map records the exact alias, not a prefix family.
	near := []byte(`{"type":"function_call","name":"collaboration-optimizer.spawn_agent","namespace":null}`)
	unchanged := restoreJSON(t, v2Inventory(), optimizedV2Inventory(), near)
	if gjson.GetBytes(unchanged, "name").String() != "collaboration-optimizer.spawn_agent" {
		t.Fatalf("near-prefix name was rewritten: %s", unchanged)
	}
}

func TestRestoreMultipleLifecycleToolsFromInventory(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"response":{"output":[
			{"type":"function_call","call_id":"c1","name":"multi_agent_v1.spawn_agent","namespace":null,"arguments":"{}"},
			{"type":"function_call","call_id":"c2","name":"multi_agent_v1.wait_agent","namespace":null,"arguments":"{}"},
			{"type":"function_call","call_id":"c3","name":"multi_agent_v1.send_input","namespace":null,"arguments":"{\"text\":\"hi\"}"}
		]}
	}`)
	got := restoreJSON(t, v1Inventory(), v1Inventory(), payload)
	want := []struct {
		name string
		args string
	}{
		{"spawn_agent", "{}"},
		{"wait_agent", "{}"},
		{"send_input", `{"text":"hi"}`},
	}
	for i, expected := range want {
		item := gjson.GetBytes(got, "response.output."+string(rune('0'+i)))
		if item.Get("namespace").String() != "multi_agent_v1" {
			t.Fatalf("item %d namespace = %q", i, item.Get("namespace").String())
		}
		if item.Get("name").String() != expected.name {
			t.Fatalf("item %d name = %q, want %q", i, item.Get("name").String(), expected.name)
		}
		if item.Get("arguments").String() != expected.args {
			t.Fatalf("item %d arguments changed: %q", i, item.Get("arguments").String())
		}
	}
}

func TestLegitimateDottedTopLevelFunctionUntouched(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"tools":[
			{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"}]},
			{"type":"function","name":"custom.report"}
		]
	}`)
	payload := []byte(`{
		"response":{"output":[
			{"type":"function_call","call_id":"fn1","name":"custom.report","namespace":null,"arguments":"{\"x\":1}"},
			{"type":"function_call","call_id":"fn2","name":"multi_agent_v1.spawn_agent","namespace":null,"arguments":"{}"}
		]}
	}`)
	got := restoreJSON(t, original, original, payload)
	report := gjson.GetBytes(got, "response.output.0")
	if report.Get("name").String() != "custom.report" {
		t.Fatalf("legitimate dotted function rewritten: %s", report.Raw)
	}
	if report.Get("namespace").Exists() && report.Get("namespace").Type != gjson.Null && report.Get("namespace").String() != "" {
		t.Fatalf("legitimate dotted function gained namespace: %s", report.Raw)
	}
	spawn := gjson.GetBytes(got, "response.output.1")
	if spawn.Get("namespace").String() != "multi_agent_v1" || spawn.Get("name").String() != "spawn_agent" {
		t.Fatalf("v1 spawn not restored: %s", spawn.Raw)
	}
}

func TestCollisionWithTopLevelDottedFunctionFailsClosed(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"tools":[
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]},
			{"type":"function","name":"collaboration.spawn_agent"}
		]
	}`)
	restoreMap := BuildCodexNamespaceRestoreMap(original, original)
	ambiguous := restoreMap.AmbiguousWireIdentities()
	found := false
	for _, name := range ambiguous {
		if name == "collaboration.spawn_agent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("collision was not recorded as ambiguous: %v", ambiguous)
	}

	payload := []byte(`{"type":"function_call","call_id":"c1","name":"collaboration.spawn_agent","namespace":null,"arguments":"{}"}`)
	got := RestoreCodexNamespaceToolsFromMap(payload, restoreMap)
	if gjson.GetBytes(got, "name").String() != "collaboration.spawn_agent" {
		t.Fatalf("collision was silently restored: %s", got)
	}
	if ns := gjson.GetBytes(got, "namespace"); ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
		t.Fatalf("collision gained canonical namespace: %s", got)
	}
}

func TestUnknownDottedToolUntouched(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"function_call","name":"unknown.tool","namespace":null,"arguments":"{}"}`)
	got := restoreJSON(t, v1Inventory(), v1Inventory(), payload)
	if string(got) == string(payload) {
		return
	}
	if gjson.GetBytes(got, "name").String() != "unknown.tool" {
		t.Fatalf("unknown dotted name rewritten: %s", got)
	}
}

func TestNearPrefixAndUnrelatedNamesUntouched(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"response":{"output":[
			{"type":"function_call","name":"collaboration-optimizer.spawn_agent","namespace":null},
			{"type":"function_call","name":"collaborationx.spawn_agent","namespace":null},
			{"type":"message","name":"multi_agent_v1.spawn_agent"}
		]}
	}`)
	got := restoreJSON(t, v2Inventory(), optimizedV2Inventory(), payload)
	if gjson.GetBytes(got, "response.output.0.name").String() != "collaboration-optimizer.spawn_agent" {
		t.Fatalf("near-prefix rewritten")
	}
	if gjson.GetBytes(got, "response.output.1.name").String() != "collaborationx.spawn_agent" {
		t.Fatalf("unrelated dotted name rewritten")
	}
	if gjson.GetBytes(got, "response.output.2.name").String() != "multi_agent_v1.spawn_agent" {
		t.Fatalf("non-tool item rewritten")
	}
}

func TestMalformedPayloadPassThrough(t *testing.T) {
	t.Parallel()

	restoreMap := BuildCodexNamespaceRestoreMap(v1Inventory(), v1Inventory())
	malformed := []byte(`{"type":"function_call","name":`)
	if got := RestoreCodexNamespaceToolsFromMap(malformed, restoreMap); string(got) != string(malformed) {
		t.Fatalf("malformed payload changed")
	}
	if got := RestoreCodexNamespaceToolsFromMap(nil, restoreMap); got != nil {
		t.Fatalf("nil payload changed")
	}
}

func TestStreamingOutputItemRestored(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.output_item.added",
		"output_index":0,
		"item":{
			"id":"item_1",
			"type":"function_call",
			"call_id":"call_stream_1",
			"name":"collaboration-optimize.wait_agent",
			"namespace":null,
			"arguments":"{\"timeout_ms\":1000}"
		}
	}`)
	got := restoreJSON(t, v2Inventory(), optimizedV2Inventory(), payload)
	if gjson.GetBytes(got, "type").String() != "response.output_item.added" {
		t.Fatalf("event type changed")
	}
	if gjson.GetBytes(got, "item.id").String() != "item_1" {
		t.Fatalf("item id changed")
	}
	if gjson.GetBytes(got, "item.namespace").String() != "collaboration" {
		t.Fatalf("streaming namespace = %q", gjson.GetBytes(got, "item.namespace").String())
	}
	if gjson.GetBytes(got, "item.name").String() != "wait_agent" {
		t.Fatalf("streaming name = %q", gjson.GetBytes(got, "item.name").String())
	}
	if gjson.GetBytes(got, "item.call_id").String() != "call_stream_1" {
		t.Fatalf("call_id changed")
	}
	if gjson.GetBytes(got, "item.arguments").String() != `{"timeout_ms":1000}` {
		t.Fatalf("arguments changed")
	}

	done := []byte(`{
		"type":"response.output_item.done",
		"item":{
			"type":"function_call",
			"call_id":"call_stream_1",
			"name":"collaboration-optimize.wait_agent",
			"namespace":null,
			"arguments":"{\"timeout_ms\":1000}"
		}
	}`)
	gotDone := restoreJSON(t, v2Inventory(), optimizedV2Inventory(), done)
	if gjson.GetBytes(gotDone, "item.namespace").String() != "collaboration" || gjson.GetBytes(gotDone, "item.name").String() != "wait_agent" {
		t.Fatalf("output_item.done not restored: %s", gotDone)
	}
}

func TestNonStreamingCompletedOutputRestored(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_1",
			"output":[{
				"id":"out_1",
				"type":"function_call",
				"call_id":"call_ns_1",
				"name":"multi_agent_v1.spawn_agent",
				"namespace":null,
				"arguments":"{\"message\":\"go\"}"
			}]
		}
	}`)
	got := restoreJSON(t, v1Inventory(), v1Inventory(), payload)
	if gjson.GetBytes(got, "response.id").String() != "resp_1" {
		t.Fatalf("response id changed")
	}
	item := gjson.GetBytes(got, "response.output.0")
	if item.Get("id").String() != "out_1" || item.Get("call_id").String() != "call_ns_1" {
		t.Fatalf("ids changed: %s", item.Raw)
	}
	if item.Get("namespace").String() != "multi_agent_v1" || item.Get("name").String() != "spawn_agent" {
		t.Fatalf("non-streaming restore failed: %s", item.Raw)
	}
}

func TestMapIsInventoryDrivenNotNamespaceConstants(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"tools":[{
			"type":"namespace",
			"name":"custom_ns",
			"tools":[{"type":"function","name":"do_work"}]
		}]
	}`)
	payload := []byte(`{"type":"function_call","name":"custom_ns.do_work","namespace":null,"arguments":"{}"}`)
	got := restoreJSON(t, original, original, payload)
	if gjson.GetBytes(got, "namespace").String() != "custom_ns" || gjson.GetBytes(got, "name").String() != "do_work" {
		t.Fatalf("inventory-driven restore failed: %s", got)
	}
}

func TestEmptyMapLeavesPayloadUnchanged(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"function_call","name":"multi_agent_v1.spawn_agent","namespace":null}`)
	if got := RestoreCodexNamespaceToolsFromMap(payload, nil); string(got) != string(payload) {
		t.Fatalf("nil map changed payload")
	}
	if got := RestoreCodexNamespaceToolsFromMap(payload, BuildCodexNamespaceRestoreMap([]byte(`{"tools":[]}`), nil)); string(got) != string(payload) {
		t.Fatalf("empty inventory changed payload")
	}
}

func TestBareUnprovenShortChildRemainsUntouched(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"function_call","call_id":"call_wait","name":"wait_agent","namespace":null,"arguments":"{\"timeout_ms\":1000}"}`)
	got := restoreJSON(t, v1Inventory(), v1Inventory(), payload)
	if gjson.GetBytes(got, "name").String() != "wait_agent" {
		t.Fatalf("bare unproven short child name rewritten: %s", got)
	}
	if ns := gjson.GetBytes(got, "namespace"); ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
		t.Fatalf("unproven short child gained namespace: %s", got)
	}
	if gjson.GetBytes(got, "call_id").String() != "call_wait" {
		t.Fatalf("call_id changed")
	}
}

func TestAmbiguousShortChildNameFailsClosed(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"tools":[
			{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"wait_agent"}]},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"wait_agent"}]}
		]
	}`)
	payload := []byte(`{"type":"function_call","name":"wait_agent","namespace":null,"arguments":"{}"}`)
	got := restoreJSON(t, original, original, payload)
	if gjson.GetBytes(got, "name").String() != "wait_agent" {
		t.Fatalf("ambiguous short name rewritten: %s", got)
	}
	if ns := gjson.GetBytes(got, "namespace"); ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
		t.Fatalf("ambiguous short name gained a namespace: %s", got)
	}
}

func TestOptimizedAliasDoesNotGuessPrefix(t *testing.T) {
	t.Parallel()

	restoreMap := BuildCodexNamespaceRestoreMap(v2Inventory(), optimizedV2Inventory())
	if restoreMap == nil {
		t.Fatal("expected map")
	}
	if _, ok := restoreMap.lookupDotted("collaboration-optimize.spawn_agent"); !ok {
		t.Fatal("exact optimizer alias was not recorded from the request")
	}
	if _, ok := restoreMap.lookupDotted("collaboration.spawn_agent"); !ok {
		t.Fatal("canonical dotted identity was not recorded")
	}
	for _, name := range restoreMap.AmbiguousWireIdentities() {
		if strings.Contains(name, "collaboration-optimize") {
			t.Fatalf("exact optimizer alias marked ambiguous: %s", name)
		}
	}
	prefixFamily := []byte(`{"type":"function_call","name":"collaboration-optimizeX.spawn_agent","namespace":null}`)
	got := RestoreCodexNamespaceToolsFromMap(prefixFamily, restoreMap)
	if gjson.GetBytes(got, "name").String() != "collaboration-optimizeX.spawn_agent" {
		t.Fatalf("prefix-family name restored: %s", got)
	}
}
