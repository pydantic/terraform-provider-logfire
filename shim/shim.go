// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package shim

import (
	tfpfprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/pydantic/terraform-provider-logfire/internal/provider"
)

// Option customizes the provider that New returns.
type Option = provider.Option

// WithDashboardManager names the Go program that embeds the provider, such as
// the Pulumi bridge. Logfire shows it as the manager of the dashboards the
// provider writes. The default is Terraform.
func WithDashboardManager(tool string) Option {
	return provider.WithDashboardManager(tool)
}

// New exposes the upstream provider constructor for external consumers.
func New(version string, opts ...Option) func() tfpfprovider.Provider {
	return provider.New(version, opts...)
}
