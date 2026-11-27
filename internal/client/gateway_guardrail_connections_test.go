// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An upstream error body that repeats the token where the client cuts the body
// must not show the first part of the token.
func TestGatewayGuardrailConnectionErrorHidesCutToken(t *testing.T) {
	const token = "synthetic-presidio-token"
	for _, kept := range []int{1, len(token) / 2, len(token) - 1, len(token)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			prefix := strings.Repeat("x", maxErrorBodySize-kept)
			_, _ = w.Write([]byte(prefix + token + " and more"))
		}))
		c, err := NewAPIClient(server.URL, "test-token", server.Client())
		if err != nil {
			t.Fatal(err)
		}
		secret := token
		_, err = c.CreateGatewayGuardrailConnection(t.Context(), GatewayGuardrailConnectionCreate{
			Name: "presidio", Provider: "presidio", BaseURL: "https://presidio.example.com",
			Auth: GatewayGuardrailConnectionAuth{Type: "bearer", Token: &secret}, TimeoutMS: 2000,
		})
		server.Close()
		message := err.Error()
		if !strings.HasSuffix(message, "x<redacted>"+truncatedSuffix) {
			t.Fatalf("kept %d bytes of the token: error ends %q", kept, message[len(message)-60:])
		}
	}
}
