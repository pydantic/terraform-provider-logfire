// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import "testing"

func TestAccWriteTokenResource(t *testing.T) {
	t.Parallel()
	testAccTokenResource(t, "write")
}
