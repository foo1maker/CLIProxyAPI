package multiagentv2

import (
	"fmt"
	"os"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NamespaceFlatAliasGateEnv is the temporary pilot gate for the request-side
// namespace flat-alias representation. It is read once per request inside
// FlatAliasNamespaceTools; there is no init-time or cached gate state. When the
// gate is off the request bytes are returned unchanged.
const NamespaceFlatAliasGateEnv = "CPA_NAMESPACE_FLAT_ALIAS"

// codexFlatAliasOrigin records which namespace/child pair produced an emitted
// alias so duplicate declarations dedupe while distinct pairs collide.
type codexFlatAliasOrigin struct {
	namespace string
	child     string
}

type codexFlatAliasTool struct {
	alias  string
	origin codexFlatAliasOrigin
	raw    []byte
}

type codexFlatAliasAppend struct {
	path  string
	tools [][]byte
}

// FlatAliasNamespaceTools appends explicit top-level function tools named
// `<namespace>__<child>` for every `type:"namespace"` container in the
// optimized request inventory (top-level `tools` and
// `input[].additional_tools[].tools`), mirroring each child's definition, and
// leaves the namespace containers themselves untouched.
//
// The transform is generic: namespace names, child names, aliases, and
// collisions all derive from the current request inventory. A namespace whose
// generated alias equals an existing top-level function or container name, a
// dotted wire identity, or another namespace's alias is skipped entirely, so no
// ambiguous identity is ever emitted. The transform is idempotent: a second
// pass sees the aliases already present and emits nothing.
func FlatAliasNamespaceTools(original, optimized []byte) ([]byte, bool) {
	if os.Getenv(NamespaceFlatAliasGateEnv) != "1" {
		return optimized, false
	}
	if len(optimized) == 0 || !gjson.ValidBytes(optimized) {
		return optimized, false
	}

	existing := make(map[string]struct{})
	collectTopLevelFunctionNames(original, existing)
	collectTopLevelFunctionNames(optimized, existing)
	recordNamespaceContainerNames(optimized, existing)
	forbidden := namespaceDottedWireKeys(original, optimized)

	var appends []codexFlatAliasAppend
	emitted := make(map[string]codexFlatAliasOrigin)

	processArray := func(path string, tools gjson.Result) {
		aliasTools, ok := namespaceFlatAliasToolsForArray(tools, existing, forbidden, emitted)
		if !ok || len(aliasTools) == 0 {
			return
		}
		raws := make([][]byte, 0, len(aliasTools))
		for _, tool := range aliasTools {
			raws = append(raws, tool.raw)
		}
		appends = append(appends, codexFlatAliasAppend{path: path, tools: raws})
	}

	processArray("tools", gjson.GetBytes(optimized, "tools"))
	input := gjson.GetBytes(optimized, "input")
	if input.IsArray() {
		for index, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
				continue
			}
			processArray(fmt.Sprintf("input.%d.tools", index), item.Get("tools"))
		}
	}
	if len(appends) == 0 {
		return optimized, false
	}

	updated := optimized
	for _, target := range appends {
		merged := appendNamespaceFlatAliasTools(gjson.GetBytes(updated, target.path), target.tools)
		if merged == nil {
			return optimized, false
		}
		next, errSet := sjson.SetRawBytes(updated, target.path, merged)
		if errSet != nil {
			return optimized, false
		}
		updated = next
	}
	return updated, true
}

func namespaceFlatAliasToolsForArray(tools gjson.Result, existing, forbidden map[string]struct{}, emitted map[string]codexFlatAliasOrigin) ([]codexFlatAliasTool, bool) {
	if !tools.IsArray() {
		return nil, true
	}
	var aliasTools []codexFlatAliasTool
	for _, namespaceTool := range tools.Array() {
		if strings.TrimSpace(namespaceTool.Get("type").String()) != "namespace" {
			continue
		}
		wireName := strings.TrimSpace(namespaceTool.Get("name").String())
		if wireName == "" {
			continue
		}
		namespaceAliases, dropped := namespaceFlatAliasesForTool(wireName, namespaceTool.Get("tools"), existing, forbidden, emitted)
		if dropped {
			continue
		}
		aliasTools = append(aliasTools, namespaceAliases...)
	}
	return aliasTools, true
}

func namespaceFlatAliasesForTool(wireName string, children gjson.Result, existing, forbidden map[string]struct{}, emitted map[string]codexFlatAliasOrigin) ([]codexFlatAliasTool, bool) {
	if !children.IsArray() {
		return nil, false
	}
	var aliasTools []codexFlatAliasTool
	seen := make(map[string]struct{})
	for _, child := range children.Array() {
		if strings.TrimSpace(child.Get("type").String()) != "function" {
			continue
		}
		childName := strings.TrimSpace(child.Get("name").String())
		alias := namespaceFlatAliasName(wireName, childName)
		if alias == "" {
			continue
		}
		if _, dup := seen[alias]; dup {
			continue
		}
		seen[alias] = struct{}{}
		origin := codexFlatAliasOrigin{namespace: wireName, child: childName}
		if _, ok := existing[alias]; ok {
			return nil, true
		}
		if _, ok := forbidden[alias]; ok {
			return nil, true
		}
		if prior, ok := emitted[alias]; ok {
			if prior != origin {
				return nil, true
			}
			continue
		}
		raw, ok := namespaceFlatAliasToolRaw(child, alias)
		if !ok {
			return nil, true
		}
		aliasTools = append(aliasTools, codexFlatAliasTool{alias: alias, origin: origin, raw: raw})
	}
	for _, tool := range aliasTools {
		emitted[tool.alias] = tool.origin
	}
	return aliasTools, false
}

// namespaceFlatAliasName builds the flat alias wire name for a namespace child
// following the existing generic `__` qualifier precedent: empty or globally
// unique (`mcp__`) children and children already qualified by the namespace get
// no alias, and a namespace ending in `__` does not gain a second separator.
func namespaceFlatAliasName(namespaceName, childName string) string {
	namespaceName = strings.TrimSpace(namespaceName)
	childName = strings.TrimSpace(childName)
	if namespaceName == "" || childName == "" {
		return ""
	}
	if strings.HasPrefix(childName, "mcp__") {
		return ""
	}
	if strings.HasPrefix(childName, namespaceName+"__") || strings.HasPrefix(childName, namespaceName+".") {
		return ""
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

// namespaceFlatAliasToolRaw mirrors a namespace child as a plain top-level
// function tool under the alias name, reusing the child's raw definition
// (description, parameters, and any other fields) so the model sees an
// equivalent schema.
func namespaceFlatAliasToolRaw(child gjson.Result, alias string) ([]byte, bool) {
	if child.Raw == "" {
		return nil, false
	}
	named, errSet := sjson.SetBytes([]byte(child.Raw), "name", alias)
	if errSet != nil || len(named) == 0 {
		return nil, false
	}
	return named, true
}

// appendNamespaceFlatAliasTools returns the tools array re-serialized with the
// alias tools appended after the existing elements, or nil when the input is
// not an array.
func appendNamespaceFlatAliasTools(array gjson.Result, tools [][]byte) []byte {
	if !array.IsArray() {
		return nil
	}
	merged := make([]byte, 0, len(array.Raw)+2)
	merged = append(merged, '[')
	for i, element := range array.Array() {
		if i > 0 {
			merged = append(merged, ',')
		}
		merged = append(merged, element.Raw...)
	}
	for _, tool := range tools {
		if len(merged) > 1 {
			merged = append(merged, ',')
		}
		merged = append(merged, tool...)
	}
	merged = append(merged, ']')
	return merged
}

// recordNamespaceContainerNames adds every namespace container name in the
// payload to dst so generated aliases never shadow a container name.
func recordNamespaceContainerNames(payload []byte, dst map[string]struct{}) {
	if len(payload) == 0 || dst == nil {
		return
	}
	recordNamespaceContainerNamesFromTools(gjson.GetBytes(payload, "tools"), dst)
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
			continue
		}
		recordNamespaceContainerNamesFromTools(item.Get("tools"), dst)
	}
}

func recordNamespaceContainerNamesFromTools(tools gjson.Result, dst map[string]struct{}) {
	if !tools.IsArray() {
		return
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) != "namespace" {
			continue
		}
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			continue
		}
		dst[name] = struct{}{}
	}
}

// namespaceDottedWireKeys collects every exact dotted wire identity the request
// inventory produces so generated aliases never shadow an existing dotted
// restore key.
func namespaceDottedWireKeys(original, optimized []byte) map[string]struct{} {
	keys := make(map[string]struct{})
	pairNamespaceInventories(original, optimized, func(canonicalName, wireName string, childNames []string) {
		for _, child := range childNames {
			if child == "" {
				continue
			}
			if canonicalName != "" {
				keys[canonicalName+"."+child] = struct{}{}
			}
			if wireName != "" && wireName != canonicalName {
				keys[wireName+"."+child] = struct{}{}
			}
		}
	})
	return keys
}

// registerFlatAliasIdentities records the exact flat aliases present in the
// post-transform body so response-side restore maps them back to canonical
// identities. Only names created for this request are registered: a name that
// already existed as a top-level function in the pre-transform body is recorded
// ambiguous, and a name absent from the post-transform body is not registered,
// so restore never touches a wire name the transform refused to create.
func registerFlatAliasIdentities(original, optimized []byte, m *CodexNamespaceRestoreMap) {
	if m == nil {
		return
	}
	postNames := make(map[string]struct{})
	collectTopLevelFunctionNames(optimized, postNames)
	possible := false
	for name := range postNames {
		if strings.Contains(name, "__") {
			possible = true
			break
		}
	}
	if !possible {
		return
	}
	preNames := make(map[string]struct{})
	collectTopLevelFunctionNames(original, preNames)
	pairNamespaceInventories(original, optimized, func(canonicalName, wireName string, childNames []string) {
		if canonicalName == "" {
			return
		}
		for _, child := range childNames {
			if child == "" {
				continue
			}
			alias := namespaceFlatAliasName(wireName, child)
			if alias == "" {
				continue
			}
			if _, onWire := postNames[alias]; !onWire {
				continue
			}
			if _, preExisting := preNames[alias]; preExisting {
				m.ambiguous[alias] = struct{}{}
				delete(m.dotted, alias)
				continue
			}
			m.registerFlatAlias(alias, CodexNamespaceIdentity{Namespace: canonicalName, Name: child})
		}
	})
}

// registerFlatAlias records a flat alias wire name created by this request's
// transform. Unlike a dotted identity, a flat alias legitimately is a
// top-level function name in the post-transform body, so the top-level
// collision check does not apply; distinct identities sharing one alias name
// still fail closed.
func (m *CodexNamespaceRestoreMap) registerFlatAlias(wire string, ident CodexNamespaceIdentity) {
	if m == nil || wire == "" {
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