package main

import "testing"

func TestMCPExposesExternalModuleControls(t *testing.T) {
	found := map[string]toolSpec{}
	for _, tool := range mcpTools() {
		found[tool.Name] = tool
	}
	for _, name := range []string{"external_modules", "external_module_check", "external_module_doctor"} {
		if _, ok := found[name]; !ok {
			t.Fatalf("missing MCP tool %s", name)
		}
	}
	doctor := found["external_module_doctor"]
	if doctor.Annotations["readOnlyHint"] != false || doctor.Annotations["destructiveHint"] != false || doctor.Annotations["openWorldHint"] != true {
		t.Fatalf("doctor annotations = %#v", doctor.Annotations)
	}
}
