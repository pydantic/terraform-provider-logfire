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
		if serveOrgToken(w, r) {
			return
		}
		route := r.Method + " " + r.URL.Path
		api.requests[route]++
		projectLookup := r.URL.Path == "/api/v1/projects/"+spendingLimitsProjectID+"/" && r.Method == http.MethodGet
		if api.projectDenied && (projectLookup || r.URL.Path == spendingLimitsProjectURL) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"You do not have permission to access this project"}`))
			return
		}
		if projectLookup {
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

// projectLimitsBody names the project that the project spending limits resource configures.
func projectLimitsBody(body string) string {
	return "project_id = \"" + spendingLimitsProjectID + "\"\n" + body
}

// spendingLimitsBodies holds a valid configuration body for each resource kind.
var spendingLimitsBodies = map[string]string{
	"gateway_spending_limits":         "daily_usd = 1",
	"gateway_project_spending_limits": projectLimitsBody("daily_usd = 1"),
}

func TestLifecycleGatewaySpendingLimits(t *testing.T) {
	t.Parallel()
	server, api := newSpendingLimitsAPI(t)
	// Someone set a weekly limit in the UI before Terraform took over the organization.
	api.limits[spendingLimitsOrgURL] = logclient.GatewaySpendingLimits{WeeklyUSD: new(int64(700))}
	config := func(body string) string { return lifecycleConfig(server.URL, "gateway_spending_limits", body) }
	const resourceName = "logfire_gateway_spending_limits.test"
	runUnitTest(t,
		resource.TestStep{
			Config: config("monthly_usd = 5000"),
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(resourceName, "id", "organization"),
				resource.TestCheckResourceAttr(resourceName, "monthly_usd", "5000"),
				resource.TestCheckNoResourceAttr(resourceName, "weekly_usd"),
				resource.TestCheckNoResourceAttr(resourceName, "daily_usd"),
			),
		},
		resource.TestStep{
			Config:           config("daily_usd = 100"),
			ConfigPlanChecks: expectAction(resourceName, plancheck.ResourceActionUpdate),
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(resourceName, "daily_usd", "100"),
				resource.TestCheckNoResourceAttr(resourceName, "monthly_usd"),
			),
		},
		resource.TestStep{Config: config("daily_usd = 100"), ResourceName: resourceName, ImportState: true, ImportStateId: "organization", ImportStateVerify: true},
	)
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
	config := lifecycleConfig(server.URL, "gateway_project_spending_limits", projectLimitsBody("daily_usd = 10\nweekly_usd = 50"))
	const resourceName = "logfire_gateway_project_spending_limits.test"
	runUnitTest(t,
		resource.TestStep{
			Config: config,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(resourceName, "id", spendingLimitsProjectID),
				resource.TestCheckResourceAttr(resourceName, "daily_usd", "10"),
				resource.TestCheckResourceAttr(resourceName, "weekly_usd", "50"),
			),
		},
		resource.TestStep{Config: config, ResourceName: resourceName, ImportState: true, ImportStateId: spendingLimitsProjectID, ImportStateVerify: true},
		resource.TestStep{
			// A limit changed outside Terraform shows as drift.
			PreConfig: func() {
				api.Lock()
				defer api.Unlock()
				limits := api.limits[spendingLimitsProjectURL]
				limits.MonthlyUSD = new(int64(999))
				api.limits[spendingLimitsProjectURL] = limits
			},
			Config:           config,
			ConfigPlanChecks: expectAction(resourceName, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckNoResourceAttr(resourceName, "monthly_usd"),
		},
	)
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
	config := lifecycleConfig(server.URL, "gateway_project_spending_limits", projectLimitsBody("monthly_usd = 10"))
	const resourceName = "logfire_gateway_project_spending_limits.test"
	setDenied := func(denied bool) func() {
		return func() {
			api.Lock()
			defer api.Unlock()
			api.projectDenied = denied
		}
	}
	runUnitTest(t,
		resource.TestStep{Config: config},
		resource.TestStep{
			PreConfig:   setDenied(true),
			Config:      config,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`You do not have permission to access this\s+project`),
		},
		resource.TestStep{
			PreConfig:        setDenied(false),
			Config:           config,
			ConfigPlanChecks: expectAction(resourceName, plancheck.ResourceActionNoop),
			Check:            resource.TestCheckResourceAttr(resourceName, "monthly_usd", "10"),
		},
	)
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
	for kind, body := range spendingLimitsBodies {
		runUnitTest(t, resource.TestStep{
			Config:      lifecycleConfig(server.URL, kind, body),
			ExpectError: regexp.MustCompile(`requires\s+Logfire\s+v2026-10-16\.01\s+or\s+newer`),
		})
	}
}

func TestLifecycleGatewaySpendingLimitsValidation(t *testing.T) {
	t.Parallel()
	steps := []resource.TestStep{}
	for _, tc := range []struct{ kind, body, message string }{
		{"gateway_spending_limits", "daily_usd = 0", "between 1 and 500000000"},
		{"gateway_spending_limits", "monthly_usd = 500000001", "between 1 and 500000000"},
		{"gateway_project_spending_limits", `project_id = "demo"`, "must be a lowercase project UUID"},
		{"gateway_project_spending_limits", `project_id = "` + strings.ToUpper(spendingLimitsProjectID) + `"`, "must be a lowercase project UUID"},
	} {
		steps = append(steps, resource.TestStep{
			Config:      lifecycleConfig("http://127.0.0.1:1", tc.kind, tc.body),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(tc.message),
		})
	}
	runUnitTest(t, steps...)
}

func TestGatewaySpendingLimitsImportRejectsOtherIDs(t *testing.T) {
	t.Parallel()
	server, _ := newSpendingLimitsAPI(t)
	for _, tc := range []struct{ kind, id string }{
		{"gateway_spending_limits", "org-123"},
		{"gateway_project_spending_limits", "demo"},
		{"gateway_project_spending_limits", strings.ToUpper(spendingLimitsProjectID)},
	} {
		runUnitTest(t, resource.TestStep{
			Config: lifecycleConfig(server.URL, tc.kind, spendingLimitsBodies[tc.kind]), ResourceName: "logfire_" + tc.kind + ".test",
			ImportState: true, ImportStateId: tc.id, ExpectError: regexp.MustCompile("Invalid import ID"),
		})
	}
}
