// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// gatewayGuardrailsMinimumRelease is the first Logfire release with the public
// guardrail and guardrail connection APIs.
const gatewayGuardrailsMinimumRelease = "v2026-11-27.01"

// GatewayGuardrailTargetScope selects the requests a guardrail checks. The type
// "all" selects every endpoint. The type "endpoint" selects one endpoint by slug.
type GatewayGuardrailTargetScope struct {
	Type         string `json:"type"`
	EndpointSlug string `json:"endpoint_slug,omitempty"`
}

// GatewayGuardrailTarget applies a guardrail action to one target scope.
type GatewayGuardrailTarget struct {
	Target GatewayGuardrailTargetScope `json:"target"`
	Action string                      `json:"action"`
}

// GatewayGuardrailSampleMessage is one editor sample of a prebuilt or regex guardrail.
type GatewayGuardrailSampleMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// findGatewayGuardrailByName returns the first item of a collection page
// filtered by an organization-unique name. The collection routes have no 404 of
// their own, so a 404 means that the instance predates the guardrail APIs.
func findGatewayGuardrailByName[P any, T any](ctx context.Context, c *APIClient, collection, noun, name string, items func(*P) []T) (*T, error) {
	query := url.Values{"name": []string{name}, "limit": []string{"1"}}
	page, err := request[P](ctx, c, http.MethodGet, collection+"?"+query.Encode(), nil)
	if err != nil {
		return nil, unavailableIfNotFound(err, http.MethodGet, collection, gatewayGuardrailsMinimumRelease, "")
	}
	found := items(page)
	if len(found) == 0 {
		return nil, &APIError{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("%s with name %q not found in the credential's organization", noun, name),
		}
	}
	return &found[0], nil
}
