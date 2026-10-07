// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"encoding/json"
	"testing"
)

func TestUpdatePayloadFieldPresence(t *testing.T) {
	t.Parallel()
	name := "renamed"
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"empty project update", ProjectUpdate{}, `{}`},
		{"project rename", ProjectUpdate{ProjectName: &name}, `{"project_name":"renamed"}`},
		{"clear project description", ProjectUpdate{Description: NullableFieldValue("")}, `{"description":""}`},
		{"clear project visibility", ProjectUpdate{Visibility: NullableFieldNull[string]()}, `{"visibility":null}`},
		{"empty channel update", ChannelUpdate{}, `{}`},
		{"channel label", ChannelUpdate{Label: NullableFieldValue(name)}, `{"label":"renamed"}`},
		{"deactivate channel", ChannelUpdate{Active: NullableFieldValue(false)}, `{"active":false}`},
		{"explicit channel nulls", ChannelUpdate{Label: NullableFieldNull[string](), Active: NullableFieldNull[bool]()}, `{"label":null,"active":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("update JSON = %s; want %s", got, tc.want)
			}
		})
	}
}
