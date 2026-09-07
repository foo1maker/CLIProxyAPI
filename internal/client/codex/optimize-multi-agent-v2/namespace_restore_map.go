package multiagentv2

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// CodexNamespaceIdentity is the canonical Codex dispatch pair.
type CodexNamespaceIdentity struct {
	Namespace string
	Name      string
}

// CodexNamespaceRestoreMap is a request-scoped reversible mapping from exact
// wire identities (including aliases CPA itself created on this request) back
// to canonical (namespace, name). It is not a prefix table.
type CodexNamespaceRestoreMap struct {
	dotted         map[string]CodexNamespaceIdentity
	namespaceAlias map[string]string
	children       map[string]map[string]struct{}
	topLevel       map[string]struct{}
	ambiguous      map[string]struct{}
}

// BuildCodexNamespaceRestoreMap records canonical namespace children from the
// pre-transform inventory and any exact namespace rename present in the
// post-transform body. Hand-written V1/V2 prefix lists are not used.
func BuildCodexNamespaceRestoreMap(original, optimized []byte) *CodexNamespaceRestoreMap {
	if len(optimized) == 0 {
		optimized = original
	}
	if len(original) == 0 {
		original = optimized
	}
	m := &CodexNamespaceRestoreMap{
		dotted:         make(map[string]CodexNamespaceIdentity),
		namespaceAlias: make(map[string]string),
		children:       make(map[string]map[string]struct{}),
		topLevel:       make(map[string]struct{}),
		ambiguous:      make(map[string]struct{}),
	}
	collectTopLevelFunctionNames(original, m.topLevel)
	collectTopLevelFunctionNames(optimized, m.topLevel)
	pairNamespaceInventories(original, optimized, func(canonicalName, wireName string, childNames []string) {
		if canonicalName == "" || len(childNames) == 0 {
			return
		}
		childSet := m.children[canonicalName]
		if childSet == nil {
			childSet = make(map[string]struct{}, len(childNames))
			m.children[canonicalName] = childSet
		}
		if wireName != "" && wireName != canonicalName {
			m.namespaceAlias[wireName] = canonicalName
		}
		for _, child := range childNames {
			if child == "" {
				continue
			}
			childSet[child] = struct{}{}
			ident := CodexNamespaceIdentity{Namespace: canonicalName, Name: child}
			m.registerDotted(canonicalName+"."+child, ident)
			if wireName != "" && wireName != canonicalName {
				m.registerDotted(wireName+"."+child, ident)
			}
		}
	})
	registerFlatAliasIdentities(original, optimized, m)
	if len(m.dotted) == 0 && len(m.namespaceAlias) == 0 && len(m.ambiguous) == 0 {
		return nil
	}
	return m
}

// AmbiguousWireIdentities returns exact wire names that collided with a
// legitimate top-level function or with another canonical identity. Restore
// leaves these names untouched.
func (m *CodexNamespaceRestoreMap) AmbiguousWireIdentities() []string {
	if m == nil || len(m.ambiguous) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.ambiguous))
	for name := range m.ambiguous {
		out = append(out, name)
	}
	return out
}

func (m *CodexNamespaceRestoreMap) registerDotted(wire string, ident CodexNamespaceIdentity) {
	if m == nil || wire == "" {
		return
	}
	if _, exists := m.topLevel[wire]; exists {
		m.ambiguous[wire] = struct{}{}
		delete(m.dotted, wire)
		return
	}
	if _, alreadyAmbiguous := m.ambiguous[wire]; alreadyAmbiguous {
		return
	}
	if existing, ok := m.dotted[wire]; ok {
		if existing != ident {
			m.ambiguous[wire] = struct{}{}
			delete(m.dotted, wire)
		}
		return
	}
	m.dotted[wire] = ident
}

func (m *CodexNamespaceRestoreMap) lookupDotted(wire string) (CodexNamespaceIdentity, bool) {
	if m == nil || wire == "" {
		return CodexNamespaceIdentity{}, false
	}
	if _, ambiguous := m.ambiguous[wire]; ambiguous {
		return CodexNamespaceIdentity{}, false
	}
	ident, ok := m.dotted[wire]
	return ident, ok
}

func (m *CodexNamespaceRestoreMap) hasChild(namespace, name string) bool {
	if m == nil || namespace == "" || name == "" {
		return false
	}
	childSet, ok := m.children[namespace]
	if !ok {
		return false
	}
	_, ok = childSet[name]
	return ok
}

func (m *CodexNamespaceRestoreMap) canonicalNamespace(namespace string) (string, bool) {
	if m == nil || namespace == "" {
		return "", false
	}
	if _, ok := m.children[namespace]; ok {
		return namespace, true
	}
	if original, ok := m.namespaceAlias[namespace]; ok {
		return original, true
	}
	return "", false
}

// RestoreCodexNamespaceToolsFromMap restores tool-call items whose exact wire
// identity matches this request's map. Unknown names, ambiguous collisions, and
// non-tool fields are left untouched. Arguments and call_id are preserved.
func RestoreCodexNamespaceToolsFromMap(payload []byte, restoreMap *CodexNamespaceRestoreMap) []byte {
	if restoreMap == nil || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	if len(restoreMap.dotted) == 0 && len(restoreMap.namespaceAlias) == 0 {
		return payload
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return payload
	}
	if !restoreCodexNamespaceValue(value, restoreMap) {
		return payload
	}
	restored, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return payload
	}
	return restored
}

func restoreCodexNamespaceValue(value any, restoreMap *CodexNamespaceRestoreMap) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if restoreCodexNamespaceValue(item, restoreMap) {
				changed = true
			}
		}
	case map[string]any:
		itemType := strings.TrimSpace(mapString(typed, "type"))
		isToolCall := itemType == "function_call" || itemType == "custom_tool_call"
		if isToolCall && restoreCodexNamespaceToolCall(typed, restoreMap) {
			changed = true
		}
		for key, child := range typed {
			if key == "arguments" || key == "input" || key == "output" && (itemType == "function_call_output" || itemType == "custom_tool_call_output") {
				continue
			}
			if restoreCodexNamespaceValue(child, restoreMap) {
				changed = true
			}
		}
	}
	return changed
}

func restoreCodexNamespaceToolCall(typed map[string]any, restoreMap *CodexNamespaceRestoreMap) bool {
	name, hasName := typed["name"].(string)
	if !hasName {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	namespace := namespaceFieldString(typed)

	if ident, ok := restoreMap.lookupDotted(name); ok {
		if namespace == "" || namespace == ident.Namespace || restoreMap.namespaceAlias[namespace] == ident.Namespace {
			return applyCodexNamespaceIdentity(typed, ident)
		}
		return false
	}
	if namespace == "" {
		return false
	}
	canonical, ok := restoreMap.canonicalNamespace(namespace)
	if !ok || canonical == namespace {
		return false
	}
	if !restoreMap.hasChild(canonical, name) {
		return false
	}
	typed["namespace"] = canonical
	return true
}

func applyCodexNamespaceIdentity(typed map[string]any, ident CodexNamespaceIdentity) bool {
	changed := false
	if current := strings.TrimSpace(mapString(typed, "name")); current != ident.Name {
		typed["name"] = ident.Name
		changed = true
	}
	if current := namespaceFieldString(typed); current != ident.Namespace {
		typed["namespace"] = ident.Namespace
		changed = true
	}
	return changed
}

func namespaceFieldString(typed map[string]any) string {
	if typed == nil {
		return ""
	}
	raw, exists := typed["namespace"]
	if !exists || raw == nil {
		return ""
	}
	value, _ := raw.(string)
	return strings.TrimSpace(value)
}

func pairNamespaceInventories(original, optimized []byte, visit func(canonicalName, wireName string, children []string)) {
	pairNamespaceToolArrays(gjson.GetBytes(original, "tools"), gjson.GetBytes(optimized, "tools"), visit)

	origInput := gjson.GetBytes(original, "input")
	optInput := gjson.GetBytes(optimized, "input")
	if !origInput.IsArray() && !optInput.IsArray() {
		return
	}
	origItems := origInput.Array()
	optItems := optInput.Array()
	limit := len(origItems)
	if len(optItems) > limit {
		limit = len(optItems)
	}
	for i := 0; i < limit; i++ {
		var origItem, optItem gjson.Result
		if i < len(origItems) {
			origItem = origItems[i]
		}
		if i < len(optItems) {
			optItem = optItems[i]
		}
		origIsAdditional := strings.TrimSpace(origItem.Get("type").String()) == "additional_tools"
		optIsAdditional := strings.TrimSpace(optItem.Get("type").String()) == "additional_tools"
		if !origIsAdditional && !optIsAdditional {
			continue
		}
		pairNamespaceToolArrays(origItem.Get("tools"), optItem.Get("tools"), visit)
	}
}

func pairNamespaceToolArrays(original, optimized gjson.Result, visit func(canonicalName, wireName string, children []string)) {
	origTools := original.Array()
	optTools := optimized.Array()
	limit := len(origTools)
	if len(optTools) > limit {
		limit = len(optTools)
	}
	for i := 0; i < limit; i++ {
		var origTool, optTool gjson.Result
		if i < len(origTools) {
			origTool = origTools[i]
		}
		if i < len(optTools) {
			optTool = optTools[i]
		}
		origIsNamespace := strings.TrimSpace(origTool.Get("type").String()) == "namespace"
		optIsNamespace := strings.TrimSpace(optTool.Get("type").String()) == "namespace"
		if !origIsNamespace && !optIsNamespace {
			continue
		}
		canonical := strings.TrimSpace(origTool.Get("name").String())
		wire := strings.TrimSpace(optTool.Get("name").String())
		if canonical == "" {
			canonical = wire
		}
		if wire == "" {
			wire = canonical
		}
		children := namespaceChildNames(origTool)
		if len(children) == 0 {
			children = namespaceChildNames(optTool)
		}
		visit(canonical, wire, children)
	}
}

func namespaceChildNames(namespaceTool gjson.Result) []string {
	tools := namespaceTool.Get("tools")
	if !tools.IsArray() {
		return nil
	}
	names := make([]string, 0, len(tools.Array()))
	for _, child := range tools.Array() {
		name := strings.TrimSpace(child.Get("name").String())
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

func collectTopLevelFunctionNames(payload []byte, dst map[string]struct{}) {
	if len(payload) == 0 || dst == nil {
		return
	}
	collectTopLevelFunctionNamesFromTools(gjson.GetBytes(payload, "tools"), dst)
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
			continue
		}
		collectTopLevelFunctionNamesFromTools(item.Get("tools"), dst)
	}
}

func collectTopLevelFunctionNamesFromTools(tools gjson.Result, dst map[string]struct{}) {
	if !tools.IsArray() {
		return
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
			continue
		}
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			continue
		}
		dst[name] = struct{}{}
	}
}
