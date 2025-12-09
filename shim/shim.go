// Copyright (c) Pydantic, Inc.
// SPDX-License-Identifier: MPL-2.0

package shim

import (
	tfpfprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/pydantic/terraform-provider-logfire/internal/provider"
)

// New exposes the upstream provider constructor for external consumers.
func New(version string) func() tfpfprovider.Provider {
	return provider.New(version)
}
