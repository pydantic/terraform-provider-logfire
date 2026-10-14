// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

type apiContract struct {
	name   string
	router routers.Router
}

func loadAPIContracts(t *testing.T) []apiContract {
	t.Helper()
	directory := filepath.Join("testdata", "api-contracts")
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Contracts []struct {
			Name   string `json:"name"`
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Contracts) == 0 {
		t.Fatal("API contract manifest has no contracts")
	}
	contracts := make([]apiContract, 0, len(manifest.Contracts))
	for _, entry := range manifest.Contracts {
		data, err := os.ReadFile(filepath.Join(directory, entry.File))
		if err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(data)); actual != entry.SHA256 {
			t.Fatalf("%s checksum = %s, want %s", entry.Name, actual, entry.SHA256)
		}
		document, err := openapi3.NewLoader().LoadFromData(data)
		if err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		if !strings.HasPrefix(document.OpenAPI, "3.1.") {
			t.Fatalf("%s uses OpenAPI %s, want 3.1", entry.Name, document.OpenAPI)
		}
		if err := document.Validate(t.Context()); err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		router, err := gorillamux.NewRouter(document)
		if err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		contracts = append(contracts, apiContract{name: entry.Name, router: router})
	}
	return contracts
}

type apiContractTransport struct {
	next      http.RoundTripper
	contracts []apiContract
}

func (transport apiContractTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := readContractBody(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	options := &openapi3filter.Options{
		AuthenticationFunc:                openapi3filter.NoopAuthenticationFunc,
		SkipSettingDefaults:               true,
		IncludeResponseStatus:             true,
		RejectWhenRequestBodyNotSpecified: true,
		SchemaValidationOptions: []openapi3.SchemaValidationOption{
			openapi3.WithStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562)),
		},
	}
	inputs := make([]*openapi3filter.RequestValidationInput, 0, len(transport.contracts))
	for _, contract := range transport.contracts {
		validationRequest := request.Clone(request.Context())
		validationRequest.Body = io.NopCloser(bytes.NewReader(body))
		route, parameters, err := contract.router.FindRoute(validationRequest)
		if err != nil {
			return nil, fmt.Errorf("%s request %s %s: %w", contract.name, request.Method, request.URL.Path, err)
		}
		input := &openapi3filter.RequestValidationInput{Request: validationRequest, Route: route, PathParams: parameters, Options: options}
		if err := openapi3filter.ValidateRequest(request.Context(), input); err != nil {
			return nil, fmt.Errorf("%s request %s %s: %w", contract.name, request.Method, request.URL.Path, err)
		}
		inputs = append(inputs, input)
	}
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	responseBody, err := readContractBody(response.Body)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(responseBody))
	for index, contract := range transport.contracts {
		responses := inputs[index].Route.Operation.Responses
		if responses.Status(response.StatusCode) == nil && responses.Default() == nil {
			return nil, fmt.Errorf("%s response %s %s status %d: undocumented status", contract.name, request.Method, request.URL.Path, response.StatusCode)
		}
		input := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: inputs[index], Status: response.StatusCode, Header: response.Header, Options: options,
		}
		if err := openapi3filter.ValidateResponse(request.Context(), input.SetBodyBytes(responseBody)); err != nil {
			return nil, fmt.Errorf("%s response %s %s status %d: %w", contract.name, request.Method, request.URL.Path, response.StatusCode, err)
		}
	}
	return response, nil
}

func readContractBody(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	return io.ReadAll(body)
}

func TestAPIContractRejectsInvalidClientTraffic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		call      func(*APIClient) error
		body      string
		want      []string
		forwarded bool
	}{
		{
			name: "unknown scope",
			call: func(client *APIClient) error {
				_, err := client.CreateAPIKey(t.Context(), APIKeyCreate{Name: "otel", Scopes: []string{"organization:unknown_scope"}})
				return err
			},
			want: []string{"request POST /api/v1/api-keys/", "scopes"},
		},
		{
			name: "null nonnullable name",
			call: func(client *APIClient) error {
				_, err := client.UpdateAPIKey(t.Context(), "11111111-1111-4111-8111-111111111111", APIKeyUpdate{Name: NullableFieldNull[string]()})
				return err
			},
			want: []string{"request PATCH /api/v1/api-keys/", "name"},
		},
		{
			name: "missing required response field",
			call: func(client *APIClient) error {
				_, err := client.CreateAPIKey(t.Context(), APIKeyCreate{Name: "otel", Scopes: []string{"project:write_otlp"}})
				return err
			},
			body:      strings.Replace(apiKeyCreateResponse, `"created_by":null,`, "", 1),
			want:      []string{"response POST /api/v1/api-keys/ status 201", "created_by"},
			forwarded: true,
		},
	}
	for _, contract := range loadAPIContracts(t) {
		for _, test := range tests {
			t.Run(contract.name+"/"+test.name, func(t *testing.T) {
				forwarded := false
				client, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{
					Transport: apiContractTransport{
						contracts: []apiContract{contract},
						next:      apiKeyStubTransport{status: http.StatusCreated, body: test.body, check: func(*http.Request) { forwarded = true }},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				err = test.call(client)
				if err == nil {
					t.Fatal("invalid HTTP traffic passed contract validation")
				}
				for _, fragment := range append([]string{contract.name}, test.want...) {
					if !strings.Contains(err.Error(), fragment) {
						t.Fatalf("error = %v, want %q", err, fragment)
					}
				}
				if forwarded != test.forwarded {
					t.Fatalf("request forwarded = %t, want %t", forwarded, test.forwarded)
				}
			})
		}
	}
}
