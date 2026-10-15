// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type dashboardDefinitionPlanModifier struct{}

func (dashboardDefinitionPlanModifier) Description(context.Context) string {
	return "Preserve the prior dashboard definition when configuration is semantically equivalent."
}

func (m dashboardDefinitionPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (dashboardDefinitionPlanModifier) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	if dashboardDefinitionsEqual(req.StateValue, req.ConfigValue) {
		resp.PlanValue = req.StateValue
	}
}

type dashboardDefinitionValidator struct{}

func (dashboardDefinitionValidator) Description(context.Context) string {
	return "The dashboard definition must be a JSON object."
}

func (v dashboardDefinitionValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (dashboardDefinitionValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := decodeDefinitionObject([]byte(req.ConfigValue.ValueString())); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid dashboard definition", err.Error())
	}
}

func dashboardDefinitionsEqual(prior, current types.String) bool {
	if prior.IsNull() || prior.IsUnknown() || current.IsNull() || current.IsUnknown() {
		return false
	}
	previous, previousErr := canonicalDashboardDefinition(prior.ValueString())
	next, nextErr := canonicalDashboardDefinition(current.ValueString())
	return previousErr == nil && nextErr == nil && previous == next
}

func preserveDashboardDefinition(prior, current types.String) types.String {
	if dashboardDefinitionsEqual(prior, current) {
		return prior
	}
	return current
}

func decodeDefinitionObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid definition JSON: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("dashboard definition must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("dashboard definition must contain exactly one JSON object")
	}
	return payload, nil
}

func canonicalDashboardDefinition(raw string) (string, error) {
	payload, err := decodeDefinitionObject([]byte(raw))
	if err != nil {
		return "", err
	}
	delete(payload, "metadata")
	if spec, ok := payload["spec"].(map[string]any); ok {
		setDashboardDefault(spec, "datasources", map[string]any{})
		setDashboardDefault(spec, "annotations", []any{})
		setDashboardDefault(spec, "links", []any{})
		if spec["variables"] == nil {
			spec["variables"] = []any{}
		}
		setDashboardDefault(spec, "duration", "1h")
		setDashboardDefault(spec, "refreshInterval", "0s")
		if display, ok := spec["display"].(map[string]any); ok {
			setDashboardDefault(display, "description", nil)
		}
		if datasources, ok := spec["datasources"].(map[string]any); ok {
			for _, value := range datasources {
				if datasource, ok := value.(map[string]any); ok {
					setDashboardDefault(datasource, "default", false)
					if display, ok := datasource["display"].(map[string]any); ok {
						setDashboardDefault(display, "description", nil)
					}
				}
			}
		}
		if layouts, ok := spec["layouts"].([]any); ok {
			for _, value := range layouts {
				layout, ok := value.(map[string]any)
				if !ok || layout["kind"] != "Grid" {
					continue
				}
				layoutSpec, ok := layout["spec"].(map[string]any)
				if !ok {
					continue
				}
				setDashboardDefault(layoutSpec, "display", map[string]any{})
				if display, ok := layoutSpec["display"].(map[string]any); ok {
					setDashboardDefault(display, "title", "")
					setDashboardDefault(display, "collapse", map[string]any{})
					if collapse, ok := display["collapse"].(map[string]any); ok {
						setDashboardDefault(collapse, "open", true)
					}
				}
				items, _ := layoutSpec["items"].([]any)
				for _, value := range items {
					item, _ := value.(map[string]any)
					content, _ := item["content"].(map[string]any)
					if ref, ok := content["ref"]; ok {
						if _, aliased := content["$ref"]; !aliased {
							content["$ref"] = ref
						}
						delete(content, "ref")
					}
				}
			}
		}
	}
	normalized, err := json.Marshal(canonicalDashboardNumbers(payload))
	return string(normalized), err
}

func canonicalDashboardNumbers(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			value[key] = canonicalDashboardNumbers(child)
		}
	case []any:
		for index, child := range value {
			value[index] = canonicalDashboardNumbers(child)
		}
	case json.Number:
		if !strings.ContainsAny(string(value), ".eE") {
			if value == "-0" {
				return json.Number("0")
			}
			return value
		}
		// Platform parses decimal and exponent tokens as Python floats while
		// preserving integer tokens. Compare at the precision it stores.
		number, err := value.Float64()
		if err != nil {
			return value
		}
		if number == 0 {
			return json.Number("0")
		}
		return json.Number(strconv.FormatFloat(number, 'f', -1, 64))
	}
	return value
}

func setDashboardDefault(object map[string]any, key string, value any) {
	if _, exists := object[key]; !exists {
		object[key] = value
	}
}
