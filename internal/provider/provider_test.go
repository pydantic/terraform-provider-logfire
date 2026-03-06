// Copyright Pydantic, Inc. 2025
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

// testAccProviderConfig builds the provider config from env.
func testAccProviderConfig() string {
	base := os.Getenv("LOGFIRE_BASE_URL")
	key := os.Getenv("LOGFIRE_API_KEY")
	if base == "" {
		return fmt.Sprintf(`
provider "logfire" {
  api_key  = %q
}
`, key)
	}
	return fmt.Sprintf(`
provider "logfire" {
  base_url = %q
  api_key  = %q
}
`, base, key)
}
