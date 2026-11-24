// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"logfire": providerserver.NewProtocol6WithError(New("test")()),
}

func testAccPreCheck(t *testing.T) {
	t.Helper()

	if os.Getenv("TF_ACC") == "" {
		t.Fatalf("acceptance tests must be run with TF_ACC=1")
	}

	if v := os.Getenv("LOGFIRE_API_KEY"); v == "" {
		t.Fatalf("LOGFIRE_API_KEY must be set for acceptance tests")
	} else if os.Getenv("LOGFIRE_BASE_URL") == "" {
		if _, err := inferBaseURLFromAPIKey(v); err != nil {
			t.Fatalf("LOGFIRE_BASE_URL must be set for acceptance tests when the api key region is not inferable: %v", err)
		}
	}
}

// testAccProviderBlock builds a provider block that authenticates with key and
// points at LOGFIRE_BASE_URL when it is set.
func testAccProviderBlock(alias, key string) string {
	block := "\nprovider \"logfire\" {\n"
	if alias != "" {
		block += fmt.Sprintf("  alias    = %q\n", alias)
	}
	if base := os.Getenv("LOGFIRE_BASE_URL"); base != "" {
		block += fmt.Sprintf("  base_url = %q\n", base)
	}
	return block + fmt.Sprintf("  api_key  = %q\n}\n", key)
}

// testAccProviderConfig builds the provider config from env.
func testAccProviderConfig() string {
	return testAccProviderBlock("", os.Getenv("LOGFIRE_API_KEY"))
}

// testAccScopedProviderConfig builds an aliased provider config authenticated
// as the project-pinned API key. Only keys for the pinned test project can be
// managed through it: the public API-keys endpoint delegates solely scopes the
// caller holds, and project-bound scopes (read_otlp, write_otlp,
// gateway_proxy) can only live on a project-scoped key.
func testAccScopedProviderConfig() string {
	return testAccProviderBlock("keys", os.Getenv("LOGFIRE_SCOPED_API_KEY"))
}

// testAccScopedProjectID returns the pinned project that scoped-key tests
// manage keys in, skipping the test when the scoped credential is not
// configured (for example local runs without the pinned CI secret).
func testAccScopedProjectID(t *testing.T) string {
	t.Helper()
	if os.Getenv("LOGFIRE_SCOPED_API_KEY") == "" || os.Getenv("LOGFIRE_API_KEY_TEST_PROJECT_ID") == "" {
		t.Skip("skipping scoped API key test: LOGFIRE_SCOPED_API_KEY and LOGFIRE_API_KEY_TEST_PROJECT_ID must be set")
	}
	return os.Getenv("LOGFIRE_API_KEY_TEST_PROJECT_ID")
}
