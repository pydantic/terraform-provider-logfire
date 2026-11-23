// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const projectsCollection = "/api/v1/projects/"

type projectRequest struct {
	method string
	path   string
	// organization is the organization that the request's bearer acts in.
	organization string
}

// projectOrganizationAPI is a self-hosted instance. Every organization has its
// own projects, write tokens, and Gateway spending limits, keyed by
// organization ID so that a rename keeps them. The raw key "test-token" is
// the instance-admin key and acts in the admin organization. The raw key
// "team-a-key" belongs to team-a and cannot exchange. The token endpoint
// exchanges the instance-admin key for "org-token-<organization ID>".
type projectOrganizationAPI struct {
	sync.Mutex
	t       *testing.T
	baseURL string
	// organizations maps each organization name to its ID.
	organizations  map[string]string
	projects       map[string]map[string]map[string]any
	writeTokens    map[string]map[string]map[string]any
	spendingLimits map[string]map[string]any
	// members are the members of each organization, keyed by organization ID.
	members      map[string][]logclient.OrganizationMember
	memberWrites []string
	nextID       int
	creates      int
	deletes      int
	// forbidden counts requests for a project that lives in another organization.
	forbidden int
	// rawKeyOnlyInstanceAdmin makes the raw key hold only instance:admin, as a
	// real instance-admin key often does, so it cannot list projects.
	rawKeyOnlyInstanceAdmin bool
	// cannotReadOrganization holds the bearers that lack organization:read, so
	// they cannot say which organization they belong to.
	cannotReadOrganization map[string]bool
	// organizationReadStatus makes every request for the organization of a credential fail with that status.
	organizationReadStatus int
	// exchangeFailure is the body of a 400 answer from the token endpoint.
	exchangeFailure string
	// organizationListStatus makes the organization list answer with that status.
	organizationListStatus int
	// organizationReads counts requests for the organization of a credential.
	organizationReads int
	// otherOrganizationProjectStatus is the status for a project that lives in
	// another organization. Zero means 403, as the API answers.
	otherOrganizationProjectStatus int
	exchanges                      []url.Values
	requests                       []projectRequest
}

func newProjectOrganizationAPI(t *testing.T) (*httptest.Server, *projectOrganizationAPI) {
	t.Helper()
	api := &projectOrganizationAPI{
		t:              t,
		organizations:  map[string]string{"admin": "org-admin", "team-a": "org-team-a", "team-b": "org-team-b"},
		projects:       map[string]map[string]map[string]any{},
		writeTokens:    map[string]map[string]map[string]any{},
		spendingLimits: map[string]map[string]any{},
		members:        seededMembers(),
	}
	server := httptest.NewServer(api)
	api.baseURL = server.URL
	t.Cleanup(server.Close)
	return server, api
}

// rename renames an organization outside Terraform. It keeps the ID.
func (api *projectOrganizationAPI) rename(from, to string) {
	api.Lock()
	defer api.Unlock()
	id := api.organizations[from]
	delete(api.organizations, from)
	api.organizations[to] = id
}

// remove deletes an organization outside Terraform, with everything in it.
func (api *projectOrganizationAPI) remove(name string) {
	api.Lock()
	defer api.Unlock()
	id := api.organizations[name]
	delete(api.organizations, name)
	for projectID := range api.projects[id] {
		delete(api.writeTokens, projectID)
	}
	delete(api.projects, id)
	delete(api.spendingLimits, id)
}

func (api *projectOrganizationAPI) organizationName(id string) string {
	for name, candidate := range api.organizations {
		if candidate == id {
			return name
		}
	}
	return ""
}

func (api *projectOrganizationAPI) fail(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (api *projectOrganizationAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	api.Lock()
	defer api.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/oauth/token" {
		api.exchange(w, r)
		return
	}
	if r.URL.Path == "/api/v1/instance/organizations/" && r.Method == http.MethodGet {
		if api.organizationListStatus != 0 {
			api.fail(w, api.organizationListStatus, `{"detail":"organization list unavailable"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			api.fail(w, http.StatusForbidden, `{"detail":"This token is missing the required scope: instance:admin."}`)
			return
		}
		organizations := []map[string]any{}
		for name, id := range api.organizations {
			organizations = append(organizations, map[string]any{"id": id, "organization_name": name})
		}
		_ = json.NewEncoder(w).Encode(organizations)
		return
	}

	organizationID := ""
	authorization := r.Header.Get("Authorization")
	switch {
	case authorization == "Bearer test-token":
		organizationID = api.organizations["admin"]
	case authorization == "Bearer team-a-key":
		organizationID = "org-team-a"
	case authorization == "Bearer team-b-key":
		organizationID = "org-team-b"
	case strings.HasPrefix(authorization, "Bearer org-token-"):
		organizationID = strings.TrimPrefix(authorization, "Bearer org-token-")
	}
	if organizationID == "" || api.organizationName(organizationID) == "" {
		api.fail(w, http.StatusUnauthorized, `{"detail":"Invalid token"}`)
		return
	}
	isOrganizationRead := r.URL.Path == "/api/v1/organization/"
	if !isOrganizationRead {
		api.requests = append(api.requests, projectRequest{method: r.Method, path: r.URL.Path, organization: api.organizationName(organizationID)})
	}
	if authorization == "Bearer test-token" && api.rawKeyOnlyInstanceAdmin {
		api.fail(w, http.StatusForbidden, `{"detail":"This token is missing the required scope: project:read."}`)
		return
	}
	switch {
	case isOrganizationRead:
		api.organizationReads++
		if api.organizationReadStatus != 0 {
			api.fail(w, api.organizationReadStatus, `{"detail":"organization unavailable"}`)
			return
		}
		if api.cannotReadOrganization[authorization] {
			api.fail(w, http.StatusForbidden, `{"detail":"This token is missing the required scope: organization:read."}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": organizationID, "organization_name": api.organizationName(organizationID)})
	case strings.HasPrefix(r.URL.Path, "/api/v1/members/"):
		api.serveMembers(w, r, organizationID)
	case r.URL.Path == "/api/v1/gateway/spending-limits/":
		api.serveSpendingLimits(w, r, organizationID)
	case strings.Contains(r.URL.Path, "/write-tokens/"):
		api.serveWriteTokens(w, r, organizationID)
	case strings.HasPrefix(r.URL.Path, projectsCollection):
		api.serveProjects(w, r, organizationID)
	default:
		api.t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
		api.fail(w, http.StatusNotFound, `{"detail":"Not Found"}`)
	}
}

func (api *projectOrganizationAPI) exchange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		api.fail(w, http.StatusBadRequest, `{"error":"invalid_request"}`)
		return
	}
	api.exchanges = append(api.exchanges, r.PostForm)
	if api.exchangeFailure != "" {
		api.fail(w, http.StatusBadRequest, api.exchangeFailure)
		return
	}
	if r.PostForm.Get("subject_token") != "test-token" {
		api.fail(w, http.StatusBadRequest, `{"error":"invalid_scope","error_description":"subject_token must include instance:admin"}`)
		return
	}
	organization := strings.TrimPrefix(r.PostForm.Get("audience"), api.baseURL+"/")
	id, ok := api.organizations[organization]
	if !ok {
		api.fail(w, http.StatusBadRequest, fmt.Sprintf(`{"error":"invalid_target","error_description":"unknown organization: '%s'"}`, organization))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "org-token-" + id, "expires_in": 900})
}

// project returns the project with id in the organization, and counts a
// request for a project of another organization.
func (api *projectOrganizationAPI) project(w http.ResponseWriter, organizationID, id string) map[string]any {
	if project, ok := api.projects[organizationID][id]; ok {
		return project
	}
	for other, projects := range api.projects {
		if _, elsewhere := projects[id]; elsewhere && other != organizationID {
			api.forbidden++
			status := http.StatusForbidden
			if api.otherOrganizationProjectStatus != 0 {
				status = api.otherOrganizationProjectStatus
			}
			api.fail(w, status, `{"detail":"You do not have permission to access this project"}`)
			return nil
		}
	}
	api.fail(w, http.StatusNotFound, `{"detail":"Project not found"}`)
	return nil
}

func (api *projectOrganizationAPI) serveProjects(w http.ResponseWriter, r *http.Request, organizationID string) {
	if api.projects[organizationID] == nil {
		api.projects[organizationID] = map[string]map[string]any{}
	}
	store := api.projects[organizationID]

	if r.URL.Path == projectsCollection {
		switch r.Method {
		case http.MethodPost:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				api.fail(w, http.StatusBadRequest, `{"detail":"bad body"}`)
				return
			}
			api.creates++
			api.nextID++
			id := fmt.Sprintf("project-%d", api.nextID)
			project := map[string]any{
				"id": id, "project_name": payload["project_name"], "description": payload["description"],
				"visibility": "public", "created_at": "2026-01-01T00:00:00Z",
			}
			if visibility, ok := payload["visibility"]; ok {
				project["visibility"] = visibility
			}
			store[id] = project
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(api.projectBody(organizationID, project))
		case http.MethodGet:
			projects := []map[string]any{}
			for _, project := range store {
				projects = append(projects, api.projectBody(organizationID, project))
			}
			_ = json.NewEncoder(w).Encode(projects)
		}
		return
	}

	id := strings.Trim(strings.TrimPrefix(r.URL.Path, projectsCollection), "/")
	project := api.project(w, organizationID, id)
	if project == nil {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			api.fail(w, http.StatusBadRequest, `{"detail":"bad body"}`)
			return
		}
		for field, value := range payload {
			if value == "" {
				value = nil
			}
			project[field] = value
		}
		_ = json.NewEncoder(w).Encode(api.projectBody(organizationID, project))
	case http.MethodDelete:
		api.deletes++
		delete(store, id)
		delete(api.writeTokens, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		_ = json.NewEncoder(w).Encode(api.projectBody(organizationID, project))
	}
}

// projectBody adds the organization's current name, as the API does.
func (api *projectOrganizationAPI) projectBody(organizationID string, project map[string]any) map[string]any {
	body := map[string]any{"organization_name": api.organizationName(organizationID)}
	for field, value := range project {
		body[field] = value
	}
	return body
}

func (api *projectOrganizationAPI) serveWriteTokens(w http.ResponseWriter, r *http.Request, organizationID string) {
	projectID, tokenID, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, projectsCollection), "/write-tokens/")
	tokenID = strings.Trim(tokenID, "/")
	project := api.project(w, organizationID, projectID)
	if project == nil {
		return
	}
	if api.writeTokens[projectID] == nil {
		api.writeTokens[projectID] = map[string]map[string]any{}
	}
	tokens := api.writeTokens[projectID]
	switch {
	case r.Method == http.MethodPost && tokenID == "":
		api.nextID++
		id := fmt.Sprintf("token-%d", api.nextID)
		token := map[string]any{
			"id": id, "project_id": projectID, "project_name": project["project_name"],
			"created_at": "2026-01-01T00:00:00Z", "token_prefix": "pylf_v1_us_", "token": "pylf_v1_us_secret",
		}
		tokens[id] = token
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(token)
	case r.Method == http.MethodGet && tokenID == "":
		list := []map[string]any{}
		for _, token := range tokens {
			list = append(list, map[string]any{
				"id": token["id"], "project_id": projectID, "project_name": token["project_name"],
				"created_at": token["created_at"], "token_prefix": token["token_prefix"],
			})
		}
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == http.MethodDelete:
		if _, ok := tokens[tokenID]; !ok {
			api.fail(w, http.StatusNotFound, `{"detail":"Token not found"}`)
			return
		}
		api.deletes++
		delete(tokens, tokenID)
		w.WriteHeader(http.StatusNoContent)
	default:
		api.fail(w, http.StatusMethodNotAllowed, `{"detail":"Method Not Allowed"}`)
	}
}

func (api *projectOrganizationAPI) serveSpendingLimits(w http.ResponseWriter, r *http.Request, organizationID string) {
	limits := api.spendingLimits[organizationID]
	if limits == nil {
		limits = map[string]any{"daily_usd": nil, "weekly_usd": nil, "monthly_usd": nil}
	}
	if r.Method == http.MethodPatch {
		limits = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&limits); err != nil {
			api.fail(w, http.StatusBadRequest, `{"detail":"bad body"}`)
			return
		}
		api.spendingLimits[organizationID] = limits
	}
	_ = json.NewEncoder(w).Encode(limits)
}

// dailyLimitOf returns the daily Gateway limit of an organization, or nil.
func (api *projectOrganizationAPI) dailyLimitOf(name string) any {
	api.Lock()
	defer api.Unlock()
	return api.spendingLimits[api.organizations[name]]["daily_usd"]
}
