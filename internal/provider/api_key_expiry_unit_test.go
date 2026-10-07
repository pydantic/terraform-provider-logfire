// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAPIKeyRejectsEmptyExpiryBeforeCreation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("invalid expiry reached the API: %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key = "test-provider-token"
}
resource "logfire_api_key" "test" {
  name = "test-key"
  scopes = ["organization:read_api_key"]
  expires_at = ""
}
`, server.URL),
			ExpectError: regexp.MustCompile("expires_at must be null or a valid RFC3339 timestamp"),
		}},
	})
}
