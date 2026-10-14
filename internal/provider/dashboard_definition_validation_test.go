// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"testing"
)

func TestDashboardDefinitionRequiresObject(t *testing.T) {
	for _, definition := range []string{"null", "[]", `"text"`, "1", "true"} {
		t.Run(definition, func(t *testing.T) {
			if _, err := normalizeDefinitionRaw([]byte(definition)); err == nil {
				t.Fatalf("normalizeDefinitionRaw(%q) accepted a non-object dashboard", definition)
			}
		})
	}
	got, err := normalizeDefinitionRaw([]byte(`{"metadata":{"name":"ignored"},"spec":{}}`))
	if err != nil || got != `{"metadata":{"name":"ignored"},"spec":{}}` {
		t.Fatalf("normalizeDefinitionRaw(object) = %q, %v; want normalized object", got, err)
	}
}
