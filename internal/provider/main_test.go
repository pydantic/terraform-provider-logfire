// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"testing"
)

// TestMain clears LOGFIRE_ORGANIZATION, which would otherwise send every test
// whose provider block omits organization through the token exchange.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("LOGFIRE_ORGANIZATION")
	os.Exit(m.Run())
}
