package multiagentv2

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

const flatAliasV2Body = `{
	"tools":[
		{
			"type":"namespace",
			"name":"collaboration",
			"tools":[
				{"type":"function","name":"spawn_agent","description":"Spawns an agent","parameters":{"type":"object","properties":{"task_name":{"type":"string"}},"required":["task_name"]}},
				{"type":"function","name":"wait_agent","description":"Waits for an agent","parameters":{"type":"object","properties":{"timeout_ms":{"type":"number"}}}}
			]
		},
		{"type":"function","name":"get_weather","description":"Weather lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}
	]
}`

func flatAliasGate(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv(NamespaceFlatAliasGateEnv, map[bool]string{true: "1", false: "0"}[enabled])
}

func flatAliasBody(t *testing.T, body []byte, enabled bool) ([]byte, bool) {
	t.Helper()
	flatAliasGate(t, enabled)
	updated, changed := FlatAliasNamespaceTools(body, body)
	if !gjson.ValidBytes(updated) {
		t.Fatalf("transform produced invalid JSON: %s", updated)
	}
	return updated, changed
}

// 2. Gate OFF must be a byte-identical no-op.
func TestFlatAliasGateOffIsByteIdenticalNoOp(t *testing.T) {
	for _, gateValue := range []string{"unset", "0"} {
		body := []byte(flatAliasV2Body)
		var updated []byte
		var changed bool
		if gateValue == "unset" {
			updated, changed = FlatAliasNamespaceTools(body, body)
		} else {
			updated, changed = flatAliasBody(t, body, false)
		}
		if changed {
			t.Fatalf("gate %s: transform reported changed", gateValue)
		}
		if !bytes.Equal(updated, body) {
			t.Fatalf("gate %s: body changed: %s", gateValue, updated)
		}
	}
}

// 1. Gate ON: exact flat aliases appended, container preserved, child schema mirrored.
func TestFlatAliasAddsAliasesAndPreservesContainer(t *testing.T) {
	updated, changed := flatAliasBody(t, []byte(flatAliasV2Body), true)
	if !changed {
		t.Fatal("transform reported unchanged")
	}
	if got := gjson.GetBytes(updated, "tools.#").Int(); got != 4 {
		t.Fatalf("tools count = %d, want 4", got)
	}
	container := gjson.GetBytes(updated, "tools.0")
	if container.Get("type").String() != "namespace" || container.Get("name").String() != "collaboration" {
		t.Fatalf("namespace container altered: %s", container.Raw)
	}
	if container.Get("tools.#").Int() != 2 || container.Get("tools.0.name").String() != "spawn_agent" || container.Get("tools.1.name").String() != "wait_agent" {
		t.Fatalf("namespace children altered: %s", container.Raw)
	}
	alias := gjson.GetBytes(updated, "tools.2")
	if alias.Get("type").String() != "function" || alias.Get("name").String() != "collaboration__spawn_agent" {
		t.Fatalf("first alias wrong: %s", alias.Raw)
	}
	if alias.Get("description").String() != "Spawns an agent" {
		t.Fatalf("alias description not mirrored: %s", alias.Raw)
	}
	if alias.Get("parameters.required.0").String() != "task_name" {
		t.Fatalf("alias parameters not mirrored: %s", alias.Raw)
	}
	if got := gjson.GetBytes(updated, "tools.3.name").String(); got != "collaboration__wait_agent" {
		t.Fatalf("second alias = %q", got)
	}
	weather := gjson.GetBytes(updated, "tools.1")
	if weather.Get("name").String() != "get_weather" || weather.Get("description").String() != "Weather lookup" {
		t.Fatalf("ordinary top-level function altered: %s", weather.Raw)
	}
}

// 10. Second pass is idempotent: aliases already present are not re-added.
func TestFlatAliasSecondPassIdempotent(t *testing.T) {
	flatAliasGate(t, true)
	body := []byte(flatAliasV2Body)
	first, changedFirst := FlatAliasNamespaceTools(body, body)
	if !changedFirst {
		t.Fatal("first pass reported unchanged")
	}
	second, changedSecond := FlatAliasNamespaceTools(body, first)
	if changedSecond {
		t.Fatal("second pass reported changed")
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("second pass altered body:\nfirst:  %s\nsecond: %s", first, second)
	}
	if gjson.GetBytes(second, "tools.#").Int() != 4 {
		t.Fatalf("second pass duplicated aliases: %s", second)
	}
}

// 5. Two namespaces sharing a child produce distinct reversible aliases.
func TestFlatAliasTwoNamespacesSameChildReversible(t *testing.T) {
	pre := []byte(`{
		"tools":[
			{"type":"namespace","name":"nsA","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]},
			{"type":"namespace","name":"nsB","tools":[{"type":"function","name":"run","parameters":{"type":"object"}}]}
		]
	}`)
	post, changed := flatAliasBody(t, pre, true)
	if !changed {
		t.Fatal("transform reported unchanged")
	}
	if gjson.GetBytes(post, "tools.2.name").String() != "nsA__run" || gjson.GetBytes(post, "tools.3.name").String() != "nsB__run" {
		t.Fatalf("aliases missing: %s", post)
	}
	restoreMap := BuildCodexNamespaceRestoreMap(pre, post)
	if restoreMap == nil {
		t.Fatal("restore map nil")
	}
	payload := []byte(`{"response":{"output":[
		{"type":"function_call","call_id":"c1","name":"nsA__run","namespace":null,"arguments":"{}"},
		{"type":"function_call","call_id":"c2","name":"nsB__run","namespace":null,"arguments":"{}"}
	]}}`)
	got := RestoreCodexNamespaceToolsFromMap(payload, restoreMap)
	first := gjson.GetBytes(got, "response.output.0")
	second := gjson.GetBytes(got, "response.output.1")
	if first.Get("name").String() != "run" || first.Get("namespace").String() != "nsA" {
		t.Fatalf("nsA alias restore failed: %s", first.Raw)
	}
	if second.Get("name").String() != "run" || second.Get("namespace").String() != "nsB" {
		t.Fatalf("nsB alias restore failed: %s", second.Raw)
	}
	for _, name := range restoreMap.AmbiguousWireIdentities() {
		if name == "nsA__run" || name == "nsB__run" {
			t.Fatalf("distinct alias marked ambiguous: %s", name)
		}
	}
}

// 6. Exact collision: top-level flat name equals a generated alias => that
// namespace's aliases are dropped and restore leaves the wire name untouched.
func TestFlatAliasExactCollisionDroppedFailClosed(t *testing.T) {
	pre := []byte(`{
		"tools":[
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"wait_agent"}]},
			{"type":"function","name":"collaboration__wait_agent","description":"pre-existing flat tool"}
		]
	}`)
	post, changed := flatAliasBody(t, pre, true)
	if changed {
		t.Fatalf("colliding namespace aliases were emitted: %s", post)
	}
	if gjson.GetBytes(post, "tools.#").Int() != 2 {
		t.Fatalf("colliding namespace aliases appended: %s", post)
	}
	restoreMap := BuildCodexNamespaceRestoreMap(pre, post)
	payload := []byte(`{"type":"function_call","call_id":"c1","name":"collaboration__wait_agent","namespace":null,"arguments":"{}"}`)
	got := RestoreCodexNamespaceToolsFromMap(payload, restoreMap)
	if gjson.GetBytes(got, "name").String() != "collaboration__wait_agent" {
		t.Fatalf("collision wire name rewritten: %s", got)
	}
	if ns := gjson.GetBytes(got, "namespace"); ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
		t.Fatalf("collision wire name gained namespace: %s", got)
	}
}

// One namespace colliding does not block an independent namespace.
func TestFlatAliasCollisionIsNamespaceScoped(t *testing.T) {
	pre := []byte(`{
		"tools":[
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"wait_agent"}]},
			{"type":"namespace","name":"other","tools":[{"type":"function","name":"go"}]},
			{"type":"function","name":"collaboration__wait_agent"}
		]
	}`)
	post, changed := flatAliasBody(t, pre, true)
	if !changed {
		t.Fatal("transform reported unchanged despite non-colliding namespace")
	}
	if gjson.GetBytes(post, "tools.3.name").String() != "other__go" {
		t.Fatalf("non-colliding namespace alias missing: %s", post)
	}
	if bytes.Contains(post, []byte(`"collaboration__wait_agent","description"`)) || gjson.GetBytes(post, "tools.#").Int() != 4 {
		t.Fatalf("colliding namespace alias emitted: %s", post)
	}
	restoreMap := BuildCodexNamespaceRestoreMap(pre, post)
	if _, ok := restoreMap.lookupDotted("other__go"); !ok {
		t.Fatal("non-colliding alias not registered")
	}
	if _, ok := restoreMap.lookupDotted("collaboration__wait_agent"); ok {
		t.Fatal("dropped alias registered in map")
	}
}

// Edge cases: mcp__ children, already-qualified children, empty/blank names,
// malformed containers, and dotted wire keys are never aliased.
func TestFlatAliasSkipRules(t *testing.T) {
	pre := []byte(`{
		"tools":[
			{"type":"namespace","name":"ns","tools":[
				{"type":"function","name":"mcp__thing"},
				{"type":"function","name":"ns__old"},
				{"type":"function","name":"ns.prev"},
				{"type":"function","name":"plain"},
				{"type":"function","name":""},
				{"type":"function","name":"dup"}
			]},
			{"type":"namespace","name":"","tools":[{"type":"function","name":"orphan"}]},
			{"type":"namespace","name":"badtools","tools":"not-an-array"},
			{"type":"namespace","name":"emptykids","tools":[]},
			{"type":"namespace","name":"a.b","tools":[{"type":"function","name":"c"}]},
			{"type":"namespace","name":"ns__","tools":[{"type":"function","name":"kid"}]},
			{"type":"namespace","name":"with-dash_under.dot","tools":[{"type":"function","name":"run"}]}
		]
	}`)
	post, changed := flatAliasBody(t, pre, true)
	if !changed {
		t.Fatal("transform reported unchanged")
	}
	names := map[string]bool{}
	gjson.GetBytes(post, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("type").String() == "function" {
			names[tool.Get("name").String()] = true
		}
		return true
	})
	for _, unwanted := range []string{"ns__mcp__thing", "ns__ns__old", "ns__ns.prev", "orphan", "badtools__x", "emptykids__y"} {
		if names[unwanted] {
			t.Fatalf("forbidden alias emitted: %s", unwanted)
		}
	}
	for _, wanted := range []string{"ns__plain", "ns__dup", "a.b__c", "ns__kid", "with-dash_under.dot__run"} {
		if !names[wanted] {
			t.Fatalf("expected alias missing: %s (names=%v)", wanted, names)
		}
	}
}

// 7. Malformed input is safe pass-through.
func TestFlatAliasMalformedInputPassThrough(t *testing.T) {
	flatAliasGate(t, true)
	malformed := []byte(`{"tools":[{"type":"namespace","name":`)
	updated, changed := FlatAliasNamespaceTools(malformed, malformed)
	if changed {
		t.Fatal("malformed body reported changed")
	}
	if !bytes.Equal(updated, malformed) {
		t.Fatalf("malformed body altered: %s", updated)
	}
	if updated, changed := FlatAliasNamespaceTools(nil, nil); changed || updated != nil {
		t.Fatalf("nil body changed: %v %v", updated, changed)
	}
}

// 3 + 8. Exact alias restores to canonical on streaming and non-streaming paths,
// including the optimizer-renamed wire namespace.
func TestFlatAliasRestoreStreamingAndNonStreaming(t *testing.T) {
	pre := v2Inventory()
	post := []byte(`{
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
	flatAliasGate(t, true)
	post, changed := FlatAliasNamespaceTools(pre, post)
	if !changed {
		t.Fatal("transform reported unchanged")
	}
	if gjson.GetBytes(post, "tools.1.name").String() != "collaboration-optimize__spawn_agent" {
		t.Fatalf("optimizer-renamed alias missing: %s", post)
	}
	restoreMap := BuildCodexNamespaceRestoreMap(pre, post)

	streamAdded := []byte(`{
		"type":"response.output_item.added",
		"output_index":0,
		"item":{"id":"item_1","type":"function_call","call_id":"cs1","name":"collaboration-optimize__wait_agent","namespace":null,"arguments":"{\"timeout_ms\":1000}"}
	}`)
	gotAdded := RestoreCodexNamespaceToolsFromMap(streamAdded, restoreMap)
	if gjson.GetBytes(gotAdded, "type").String() != "response.output_item.added" {
		t.Fatalf("event type changed: %s", gotAdded)
	}
	if gjson.GetBytes(gotAdded, "item.name").String() != "wait_agent" || gjson.GetBytes(gotAdded, "item.namespace").String() != "collaboration" {
		t.Fatalf("streaming added restore failed: %s", gotAdded)
	}

	streamDone := []byte(`{
		"type":"response.output_item.done",
		"item":{"type":"function_call","call_id":"cs1","name":"collaboration-optimize__wait_agent","namespace":null,"arguments":"{\"timeout_ms\":1000}"}
	}`)
	gotDone := RestoreCodexNamespaceToolsFromMap(streamDone, restoreMap)
	if gjson.GetBytes(gotDone, "item.name").String() != "wait_agent" || gjson.GetBytes(gotDone, "item.namespace").String() != "collaboration" {
		t.Fatalf("streaming done restore failed: %s", gotDone)
	}

	completed := []byte(`{
		"type":"response.completed",
		"response":{"id":"resp_1","output":[
			{"id":"out_1","type":"function_call","call_id":"cn1","name":"collaboration-optimize__send_message","namespace":null,"arguments":"{\"message\":\"hi\"}"}
		]}
	}`)
	gotCompleted := RestoreCodexNamespaceToolsFromMap(completed, restoreMap)
	item := gjson.GetBytes(gotCompleted, "response.output.0")
	if item.Get("name").String() != "send_message" || item.Get("namespace").String() != "collaboration" {
		t.Fatalf("non-streaming restore failed: %s", item.Raw)
	}
	if item.Get("arguments").String() != `{"message":"hi"}` || item.Get("call_id").String() != "cn1" {
		t.Fatalf("arguments/call_id changed: %s", item.Raw)
	}
	if gjson.GetBytes(gotCompleted, "response.id").String() != "resp_1" {
		t.Fatalf("response id changed: %s", gotCompleted)
	}
}

// 4. With flat aliases in the map: dotted restore unchanged, unknown dotted and
// bare children untouched, legitimate dotted top-level untouched.
func TestFlatAliasCoexistsWithDottedAndLeavesUnprovenNamesUntouched(t *testing.T) {
	pre := []byte(`{
		"tools":[
			{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"},{"type":"function","name":"wait_agent"}]},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"wait_agent"}]},
			{"type":"function","name":"custom.report"}
		]
	}`)
	post, changed := flatAliasBody(t, pre, true)
	if !changed {
		t.Fatal("transform reported unchanged")
	}
	restoreMap := BuildCodexNamespaceRestoreMap(pre, post)

	payload := []byte(`{"response":{"output":[
		{"type":"function_call","call_id":"d1","name":"multi_agent_v1.spawn_agent","namespace":null,"arguments":"{}"},
		{"type":"function_call","call_id":"u1","name":"unknown.tool","namespace":null,"arguments":"{}"},
		{"type":"function_call","call_id":"b1","name":"wait_agent","namespace":null,"arguments":"{}"},
		{"type":"function_call","call_id":"l1","name":"custom.report","namespace":null,"arguments":"{}"},
		{"type":"function_call","call_id":"f1","name":"collaboration__wait_agent","namespace":null,"arguments":"{}"}
	]}}`)
	got := RestoreCodexNamespaceToolsFromMap(payload, restoreMap)
	output := gjson.GetBytes(got, "response.output")
	if output.Get("0.name").String() != "spawn_agent" || output.Get("0.namespace").String() != "multi_agent_v1" {
		t.Fatalf("existing dotted restore regressed: %s", output.Get("0").Raw)
	}
	if output.Get("1.name").String() != "unknown.tool" {
		t.Fatalf("unknown dotted rewritten: %s", output.Get("1").Raw)
	}
	if output.Get("2.name").String() != "wait_agent" {
		ns := output.Get("2.namespace")
		if ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
			t.Fatalf("bare unproven child rewritten: %s", output.Get("2").Raw)
		}
		t.Fatalf("bare child name changed: %s", output.Get("2").Raw)
	}
	if ns := output.Get("2.namespace"); ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
		t.Fatalf("bare unproven child gained namespace: %s", output.Get("2").Raw)
	}
	if output.Get("3.name").String() != "custom.report" {
		ns := output.Get("3.namespace")
		if ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
			t.Fatalf("legitimate dotted tool rewritten: %s", output.Get("3").Raw)
		}
		t.Fatalf("legitimate dotted name changed: %s", output.Get("3").Raw)
	}
	if ns := output.Get("3.namespace"); ns.Exists() && ns.Type != gjson.Null && ns.String() != "" {
		t.Fatalf("legitimate dotted tool gained namespace: %s", output.Get("3").Raw)
	}
	if output.Get("4.name").String() != "wait_agent" || output.Get("4.namespace").String() != "collaboration" {
		t.Fatalf("exact flat alias not restored: %s", output.Get("4").Raw)
	}
}

// 9. Ordinary top-level functions pass through the transform byte-preserved.
func TestFlatAliasOrdinaryTopLevelFunctionUnchanged(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"get_weather","description":"Weather lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)
	post, changed := flatAliasBody(t, body, true)
	if changed {
		t.Fatalf("body without namespace tools changed: %s", post)
	}
	if !bytes.Equal(body, post) {
		t.Fatalf("ordinary function body altered: %s", post)
	}
}