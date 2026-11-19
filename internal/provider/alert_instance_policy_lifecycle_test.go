// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const alertPolicyProjectID = "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"

type alertPolicyAPI struct {
	sync.Mutex
	alert  map[string]any
	writes []map[string]any
	denied bool
	// olderLogfire drops instance_policy like a Logfire version without instance policies.
	olderLogfire bool
}

// newAlertPolicyAPI mimics the alert API's policy handling: an omitted instance_policy keeps the stored one and null removes it.
func newAlertPolicyAPI(t *testing.T) (*httptest.Server, *alertPolicyAPI) {
	t.Helper()
	api := &alertPolicyAPI{}
	collection := "/api/v1/projects/" + alertPolicyProjectID + "/alerts/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == collection, r.Method == http.MethodPut && r.URL.Path == collection+"alert-1/":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("%s body: %v", r.Method, err)
			}
			api.writes = append(api.writes, body)
			if policy, ok := body["instance_policy"]; ok && policy != nil && api.denied {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"status":403,"error_code":"alert_instances_not_enabled"}`))
				return
			}
			if api.olderLogfire {
				delete(body, "instance_policy")
			}
			if api.alert == nil {
				api.alert = map[string]any{"id": "alert-1", "project_id": alertPolicyProjectID, "channels": []any{}, "active": true}
			}
			for key, value := range body {
				api.alert[key] = value
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			_ = json.NewEncoder(w).Encode(api.alert)
		case r.Method == http.MethodGet && r.URL.Path == collection+"alert-1/":
			_ = json.NewEncoder(w).Encode(api.alert)
		case r.Method == http.MethodDelete && r.URL.Path == collection+"alert-1/":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

func TestLifecycleAlertInstancePolicy(t *testing.T) {
	t.Parallel()
	server, api := newAlertPolicyAPI(t)
	config := func(name, notifyWhen string, policy string) string {
		return lifecycleConfig(server.URL, "alert", fmt.Sprintf(`project_id = %q
name = %q
query = "select 1"
time_window = "5m"
frequency = "1m"
notify_when = %q
channel_assignments = []
%s`, alertPolicyProjectID, name, notifyWhen, policy))
	}
	policy := func(missingRuns int) string {
		return fmt.Sprintf(`instance_policy = {
  identity_columns = ["service_name", "trace_id"]
  pending_runs     = 2
  missing_runs     = %d
  missing_behavior = "expire"
}`, missingRuns)
	}
	// lastWrite returns the most recent create or update request body.
	lastWrite := func() map[string]any {
		api.Lock()
		defer api.Unlock()
		return api.writes[len(api.writes)-1]
	}
	policyWrite := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got, err := json.Marshal(lastWrite()["instance_policy"])
			if err != nil {
				return err
			}
			if string(got) != want {
				return fmt.Errorf("instance_policy = %s, want %s", got, want)
			}
			return nil
		}
	}
	omitsPolicy := func(*terraform.State) error {
		if value, ok := lastWrite()["instance_policy"]; ok {
			return fmt.Errorf("update sent instance_policy %v, want it left out", value)
		}
		return nil
	}
	const resourceName = "logfire_alert.test"
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("alert", "instances_changed", policy(2)),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "instance_policy.identity_columns.1", "trace_id"),
					resource.TestCheckResourceAttr(resourceName, "instance_policy.missing_runs", "2"),
					policyWrite(`{"continuity_runs":2,"identity_columns":["service_name","trace_id"],"missing_behavior":"expire","missing_runs":2,"pending_runs":2}`),
				),
			},
			{
				Config: config("renamed", "instances_changed", policy(2)),
				Check:  omitsPolicy,
			},
			{
				Config: config("renamed", "instances_changed", policy(3)),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "instance_policy.missing_runs", "3"),
					policyWrite(`{"continuity_runs":3,"identity_columns":["service_name","trace_id"],"missing_behavior":"expire","missing_runs":3,"pending_runs":2}`),
				),
			},
			{
				Config: config("renamed", "has_matches", ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr(resourceName, "instance_policy"),
					policyWrite("null"),
				),
			},
			{
				Config: config("renamed again", "has_matches", ""),
				Check:  omitsPolicy,
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.denied = true
				},
				Config:      config("renamed again", "instances_changed", policy(2)),
				ExpectError: regexp.MustCompile(`alert_instances_not_enabled`),
			},
		},
	})
}

func TestLifecycleAlertInstancePolicyOnOlderLogfire(t *testing.T) {
	t.Parallel()
	server, api := newAlertPolicyAPI(t)
	api.olderLogfire = true
	config := func(policy string) string {
		return lifecycleConfig(server.URL, "alert", fmt.Sprintf(`project_id = %q
name = "alert"
query = "select 1"
time_window = "5m"
frequency = "1m"
notify_when = "has_matches"
channel_assignments = []
%s`, alertPolicyProjectID, policy))
	}
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(`instance_policy = {
  identity_columns = ["service_name"]
  pending_runs     = 1
  missing_runs     = 1
  missing_behavior = "expire"
}`),
				ExpectError: regexp.MustCompile(`does not support alert instance policies`),
			},
			{
				// The failed create still saved the alert, so Terraform replaces it.
				Config: config(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("logfire_alert.test", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
}
