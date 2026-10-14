// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
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
				layoutSpec, _ := layout["spec"].(map[string]any)
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
		mantissa := string(value)
		var exponent big.Int
		if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
			// The JSON decoder has already validated the exponent's decimal syntax.
			exponent.SetString(mantissa[index+1:], 10)
			mantissa = mantissa[:index]
		}
		negative := strings.HasPrefix(mantissa, "-")
		integer, fraction, _ := strings.Cut(strings.TrimPrefix(mantissa, "-"), ".")
		coefficient := strings.TrimLeft(integer+fraction, "0")
		if coefficient == "" {
			return json.Number("0")
		}
		digits := strings.TrimRight(coefficient, "0")
		exponent.Add(&exponent, big.NewInt(int64(len(coefficient)-len(digits)-len(fraction))))
		if negative {
			digits = "-" + digits
		}
		// Keep the exponent symbolic so large exponents allocate only their digits.
		return json.Number(digits + "e" + exponent.String())
	}
	return value
}

func setDashboardDefault(object map[string]any, key string, value any) {
	if _, exists := object[key]; !exists {
		object[key] = value
	}
}
