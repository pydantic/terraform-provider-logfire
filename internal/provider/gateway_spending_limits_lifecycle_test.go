// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	spendingLimitsProjectID  = "018f45c0-3cab-7b2f-a8f7-8a0b55a7ed33"
	spendingLimitsOrgURL     = "/api/v1/gateway/spending-limits/"
	spendingLimitsProjectURL = spendingLimitsOrgURL + "projects/" + spendingLimitsProjectID + "/"
)

type spendingLimitsAPI struct {
	sync.Mutex
	limits        map[string]logclient.GatewaySpendingLimits
	requests      map[string]int
	projectDenied bool
	routesMissing bool
}

// newSpendingLimitsAPI mimics the public spending limits API, where a PATCH changes only the windows it names.
func newSpendingLimitsAPI(t *testing.T) (*httptest.Server, *spendingLimitsAPI) {
	t.Helper()
	api := &spendingLimitsAPI{
		limits:   map[string]logclient.GatewaySpendingLimits{},
		requests: map[string]int{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.Lock()
		defer api.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-org-token","expires_in":900}`))
			return
		}
		route := r.Method + " " + r.URL.Path
		api.requests[route]++
		if r.URL.Path == "/api/v1/projects/"+spendingLimitsProjectID+"/" && r.Method == http.MethodGet {
			if api.projectDenied {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"detail":"You do not have permission to access this project"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + spendingLimitsProjectID + `","project_name":"demo","visibility":"private"}`))
			return
		}
		if r.URL.Path != spendingLimitsOrgURL && r.URL.Path != spendingLimitsProjectURL {
			t.Errorf("unexpected API request: %s", route)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if api.routesMissing {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
			return
		}
		if r.URL.Path == spendingLimitsProjectURL && api.projectDenied {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"You do not have permission to access this project"}`))
			return
		}
		limits := api.limits[r.URL.Path]
		switch r.Method {
		case http.MethodGet:
		case http.MethodPatch:
			var body map[string]*int64
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("%s body: %v", route, err)
			}
			if len(body) != 3 {
				t.Errorf("%s must name every window so Terraform owns them, got %v", route, body)
			}
			if v, ok := body["daily_usd"]; ok {
				limits.DailyUSD = v
			}
			if v, ok := body["weekly_usd"]; ok {
				limits.WeeklyUSD = v
			}
			if v, ok := body["monthly_usd"]; ok {
				limits.MonthlyUSD = v
			}
			api.limits[r.URL.Path] = limits
		default:
			t.Errorf("unexpected API request: %s", route)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(limits)
	}))
	t.Cleanup(server.Close)
	return server, api
}

func TestLifecycleGatewaySpendingLimits(t *testing.T) {
	t.Parallel()
	server, api := newSpendingLimitsAPI(t)
	// Someone set a weekly limit in the UI before Terraform took over the organization.
	api.limits[spendingLimitsOrgURL] = logclient.GatewaySpendingLimits{WeeklyUSD: new(int64(700))}
	config := func(body string) string { return lifecycleConfig(server.URL, "gateway_spending_limits", body) }
	const resourceName = "logfire_gateway_spending_limits.test"
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("monthly_usd = 5000"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", "organization"),
					resource.TestCheckResourceAttr(resourceName, "monthly_usd", "5000"),
					resource.TestCheckNoResourceAttr(resourceName, "weekly_usd"),
					resource.TestCheckNoResourceAttr(resourceName, "daily_usd"),
				),
			},
			{
				Config: config("daily_usd = 100"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "daily_usd", "100"),
					resource.TestCheckNoResourceAttr(resourceName, "monthly_usd"),
				),
			},
			{Config: config("daily_usd = 100"), ResourceName: resourceName, ImportState: true, ImportStateId: "organization", ImportStateVerify: true},
		},
	})
	api.Lock()
	defer api.Unlock()
	if got := api.limits[spendingLimitsOrgURL]; got != (logclient.GatewaySpendingLimits{}) {
		t.Fatalf("destroy left organization limits %+v", got)
	}
	if api.requests["PATCH "+spendingLimitsOrgURL] != 3 {
		t.Errorf("PATCH requests = %d, want 3 (create, update, destroy)", api.requests["PATCH "+spendingLimitsOrgURL])
	}
}

func TestLifecycleGatewayProjectSpendingLimits(t *testing.T) {
	t.Parallel()
	server, api := newSpendingLimitsAPI(t)
	config := func(body string) string {
		return lifecycleConfig(server.URL, "gateway_project_spending_limits",
			"project_id = \""+spendingLimitsProjectID+"\"\n"+body)
	}
	const resourceName = "logfire_gateway_project_spending_limits.test"
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("daily_usd = 10\nweekly_usd = 50"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "id", spendingLimitsProjectID),
					resource.TestCheckResourceAttr(resourceName, "daily_usd", "10"),
					resource.TestCheckResourceAttr(resourceName, "weekly_usd", "50"),
				),
			},
			{Config: config("daily_usd = 10\nweekly_usd = 50"), ResourceName: resourceName, ImportState: true, ImportStateId: spendingLimitsProjectID, ImportStateVerify: true},
			{
				// A limit changed outside Terraform shows as drift.
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					limits := api.limits[spendingLimitsProjectURL]
					limits.MonthlyUSD = new(int64(999))
					api.limits[spendingLimitsProjectURL] = limits
				},
				Config: config("daily_usd = 10\nweekly_usd = 50"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(resourceName, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckNoResourceAttr(resourceName, "monthly_usd"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if got := api.limits[spendingLimitsProjectURL]; got != (logclient.GatewaySpendingLimits{}) {
		t.Fatalf("destroy left project limits %+v", got)
	}
}

// The API answers a deleted project and an inaccessible project with the same 403,
// so one case covers both. Refresh keeps the state and recovers without a write.
func TestLifecycleGatewayProjectSpendingLimitsProjectUnavailable(t *testing.T) {
	t.Parallel()
	server, api := newSpendingLimitsAPI(t)
	config := lifecycleConfig(server.URL, "gateway_project_spending_limits",
		"project_id = \""+spendingLimitsProjectID+"\"\nmonthly_usd = 10")
	resource.Test(t, resource.TestCase{
		IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.projectDenied = true
				},
				Config:      config,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`You do not have permission to access this\s+project`),
			},
			{
				PreConfig: func() {
					api.Lock()
					defer api.Unlock()
					api.projectDenied = false
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("logfire_gateway_project_spending_limits.test", plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr("logfire_gateway_project_spending_limits.test", "monthly_usd", "10"),
			},
		},
	})
	api.Lock()
	defer api.Unlock()
	if got := api.requests["PATCH "+spendingLimitsProjectURL]; got != 2 {
		t.Errorf("PATCH requests = %d, want 2 (create and destroy)", got)
	}
}

func TestLifecycleGatewaySpendingLimitsOnOlderLogfire(t *testing.T) {
	t.Parallel()
	server, api := newSpendingLimitsAPI(t)
	api.routesMissing = true
	for kind, body := range map[string]string{
		"gateway_spending_limits":         "daily_usd = 1",
		"gateway_project_spending_limits": "project_id = \"" + spendingLimitsProjectID + "\"\ndaily_usd = 1",
	} {
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config:      lifecycleConfig(server.URL, kind, body),
				ExpectError: regexp.MustCompile(`requires\s+Logfire\s+v2026-10-16\.01\s+or\s+newer`),
			}},
		})
	}
}

func TestLifecycleGatewaySpendingLimitsValidation(t *testing.T) {
	t.Parallel()
	steps := []resource.TestStep{}
	for body, message := range map[string]string{
		"daily_usd = 0":           "between 1 and 500000000",
		"monthly_usd = 500000001": "between 1 and 500000000",
	} {
		steps = append(steps, resource.TestStep{
			Config:      lifecycleConfig("http://127.0.0.1:1", "gateway_spending_limits", body),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(message),
		})
	}
	for _, projectID := range []string{"demo", strings.ToUpper(spendingLimitsProjectID)} {
		steps = append(steps, resource.TestStep{
			Config:      lifecycleConfig("http://127.0.0.1:1", "gateway_project_spending_limits", `project_id = "`+projectID+`"`),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile("must be a lowercase project UUID"),
		})
	}
	resource.Test(t, resource.TestCase{IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories, Steps: steps})
}

func TestGatewaySpendingLimitsImportRejectsOtherIDs(t *testing.T) {
	t.Parallel()
	server, _ := newSpendingLimitsAPI(t)
	for _, tc := range []struct{ kind, id string }{
		{"gateway_spending_limits", "org-123"},
		{"gateway_project_spending_limits", "demo"},
		{"gateway_project_spending_limits", strings.ToUpper(spendingLimitsProjectID)},
	} {
		kind, id := tc.kind, tc.id
		body := "daily_usd = 1"
		if strings.Contains(kind, "project") {
			body = "project_id = \"" + spendingLimitsProjectID + "\"\n" + body
		}
		resource.Test(t, resource.TestCase{
			IsUnitTest: true, ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: lifecycleConfig(server.URL, kind, body), ResourceName: "logfire_" + kind + ".test",
				ImportState: true, ImportStateId: id, ExpectError: regexp.MustCompile("Invalid import ID"),
			}},
		})
	}
}
