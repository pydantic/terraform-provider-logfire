// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
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

	if v := os.Getenv("LOGFIRE_BASE_URL"); v == "" {
		t.Fatalf("LOGFIRE_BASE_URL must be set for acceptance tests")
	}
	if v := os.Getenv("LOGFIRE_API_KEY"); v == "" {
		t.Fatalf("LOGFIRE_API_KEY must be set for acceptance tests")
	}
}
