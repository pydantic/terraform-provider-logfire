// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
)

func TestDashboardDefinitionRequiresObject(t *testing.T) {
	for _, definition := range []string{"null", "[]", `"text"`, "1", "true"} {
		t.Run(definition, func(t *testing.T) {
			if _, _, err := normalizeDefinitionString(definition); err == nil {
				t.Fatalf("normalizeDefinitionString(%q) accepted a non-object dashboard", definition)
			}
			if _, err := normalizeDefinitionRaw([]byte(definition)); err == nil {
				t.Fatalf("normalizeDefinitionRaw(%q) accepted a non-object dashboard", definition)
			}
		})
	}
	got, _, err := normalizeDefinitionString(`{"metadata":{"name":"ignored"},"spec":{}}`)
	if err != nil || got != `{"metadata":{},"spec":{}}` {
		t.Fatalf("normalizeDefinitionString(object) = %q, %v; want normalized object", got, err)
	}
}
