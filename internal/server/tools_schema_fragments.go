package server

// Shared JSON-schema property fragments for the store_* / search_* / recall_*
// engineering tools (Round 3 M32). These collapse ~200 lines of duplicated
// property literals into named builders. Each returns a FRESH map so callers
// never alias shared mutable state.

// schemaStr is a string property with the given description.
func schemaStr(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// schemaTags is the shared "Additional tags" array-of-strings property.
func schemaTags() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Additional tags"}
}

// schemaImportance is the shared importance property (0..1) with a per-tool default.
func schemaImportance(def float64) map[string]any {
	return map[string]any{"type": "number", "minimum": 0, "maximum": 1, "default": def}
}

func schemaContext() map[string]any  { return schemaStr("Project, task, or service context") }
func schemaService() map[string]any  { return schemaStr("Service or component name") }
func schemaSeverity() map[string]any { return schemaStr("Severity label such as sev1 or sev2") }
