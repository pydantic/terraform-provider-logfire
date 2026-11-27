// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	logclient "github.com/pydantic/terraform-provider-logfire/internal/client"
)

const (
	guardrailConnectionsURL = "/api/v1/gateway/guardrail-connections/"
	prebuiltGuardrailsURL   = "/api/v1/gateway/guardrails/prebuilt/"
	regexGuardrailsURL      = "/api/v1/gateway/guardrails/regex/"
	presidioGuardrailsURL   = "/api/v1/gateway/guardrails/presidio/"
	guardrailTestCreatedAt  = "2026-11-27T00:00:00Z"
	// unknownEndpointSlug names an endpoint that the fake API does not have.
	unknownEndpointSlug = "unknown-endpoint"
)

// guardrailTemplates is the template catalog of the fake API.
var guardrailTemplates = map[string]string{"email-addresses": "pii", "credit-cards": "pii"}

type guardrailAPI struct {
	sync.Mutex
	nextID      int
	connections map[string]*logclient.GatewayGuardrailConnectionRead
	tokens      map[string]string
	prebuilt    map[string]*logclient.GatewayPrebuiltGuardrailRead
	// prebuiltSamples holds the samples of each template, which outlive its configuration.
	prebuiltSamples map[string][]logclient.GatewayGuardrailSampleMessage
	regex           map[string]*logclient.GatewayRegexGuardrailRead
	presidio        map[string]*logclient.GatewayPresidioGuardrailRead
	requests        map[string]int
	// bodies records the decoded body of every write, in order, by route.
	bodies map[string][]map[string]any
	// echoToken makes connection writes fail with a detail that repeats the token.
	echoToken     bool
	routesMissing bool
	// planDenied answers every route as a plan without the gateway_guardrail_api feature does.
	planDenied bool
	// presidioDenied refuses connection and Presidio guardrail writes, as a plan without the
	// gateway_guardrail_presidio feature does.
	presidioDenied bool
	// enforcementDenied refuses every action other than observe, as a plan without guardrail enforcement does.
	enforcementDenied bool
	// patternCheckDown answers regex writes as Logfire does when the Gateway cannot check a pattern.
	patternCheckDown bool
}

func newGuardrailAPI(t *testing.T) (*httptest.Server, *guardrailAPI) {
	t.Helper()
	api := &guardrailAPI{
		connections:     map[string]*logclient.GatewayGuardrailConnectionRead{},
		tokens:          map[string]string{},
		prebuilt:        map[string]*logclient.GatewayPrebuiltGuardrailRead{},
		prebuiltSamples: map[string][]logclient.GatewayGuardrailSampleMessage{},
		regex:           map[string]*logclient.GatewayRegexGuardrailRead{},
		presidio:        map[string]*logclient.GatewayPresidioGuardrailRead{},
		requests:        map[string]int{},
		bodies:          map[string][]map[string]any{},
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
		var raw json.RawMessage
		if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("%s body: %v", route, err)
			}
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			api.bodies[route] = append(api.bodies[route], body)
		}
		if api.routesMissing {
			api.reply(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
			return
		}
		if api.planDenied {
			api.reply(w, http.StatusForbidden, map[string]string{
				"detail": "Gateway guardrail management through the API is available only on Growth, Enterprise Cloud, and self-hosted plans.",
			})
			return
		}
		presidioRoute := strings.HasPrefix(r.URL.Path, guardrailConnectionsURL) || strings.HasPrefix(r.URL.Path, presidioGuardrailsURL)
		if api.presidioDenied && presidioRoute && (r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut) {
			api.reply(w, http.StatusForbidden, map[string]string{
				"detail": "Presidio PII detection guardrails require the Enterprise Cloud plan, or a self-hosted deployment.",
			})
			return
		}
		if action := deniedAction(raw); api.enforcementDenied && action != "" {
			detail := "The '" + action + "' action needs the gateway_guardrail_enforcement feature, which this organization's plan does not include. Use the 'observe' action."
			if presidioRoute {
				detail = "The `" + action + "` action needs guardrail enforcement, which this organization does not have."
			}
			api.reply(w, http.StatusForbidden, map[string]string{"detail": detail})
			return
		}
		if unknownEndpoint(raw) {
			api.reply(w, http.StatusUnprocessableEntity, map[string]string{
				"detail": "Each targets[].target.endpoint_slug must name an Endpoint of this organization. Not found: '" + unknownEndpointSlug + "'.",
			})
			return
		}
		if api.patternCheckDown && strings.HasPrefix(r.URL.Path, regexGuardrailsURL) && raw != nil {
			w.Header().Set("Retry-After", "30")
			api.reply(w, http.StatusServiceUnavailable, map[string]string{
				"detail": "The pattern could not be checked right now. Try saving again shortly.",
			})
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, guardrailConnectionsURL):
			api.serveConnections(t, w, r, raw)
		case strings.HasPrefix(r.URL.Path, prebuiltGuardrailsURL):
			api.servePrebuilt(t, w, r, raw)
		case strings.HasPrefix(r.URL.Path, regexGuardrailsURL):
			api.serveRegex(t, w, r, raw)
		case strings.HasPrefix(r.URL.Path, presidioGuardrailsURL):
			api.servePresidio(t, w, r, raw)
		default:
			t.Errorf("unexpected API request: %s", route)
			api.reply(w, http.StatusNotFound, map[string]string{"detail": "Not Found"})
		}
	}))
	t.Cleanup(server.Close)
	return server, api
}

func (api *guardrailAPI) reply(w http.ResponseWriter, status int, body any) {
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (api *guardrailAPI) newID() string {
	api.nextID++
	return fmt.Sprintf("018f45c0-3cab-7b2f-a8f7-%012d", api.nextID)
}

// lastBody returns the most recent write body of a route.
func (api *guardrailAPI) lastBody(route string) map[string]any {
	api.Lock()
	defer api.Unlock()
	bodies := api.bodies[route]
	if len(bodies) == 0 {
		return nil
	}
	return bodies[len(bodies)-1]
}

func itemID(path, collection string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, collection), "/")
}

func (api *guardrailAPI) serveConnections(t *testing.T, w http.ResponseWriter, r *http.Request, raw json.RawMessage) {
	id := itemID(r.URL.Path, guardrailConnectionsURL)
	var in struct {
		Name      *string                                   `json:"name"`
		Provider  string                                    `json:"provider"`
		BaseURL   *string                                   `json:"base_url"`
		Auth      *logclient.GatewayGuardrailConnectionAuth `json:"auth"`
		TimeoutMS *int64                                    `json:"timeout_ms"`
		Enabled   *bool                                     `json:"enabled"`
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &in)
	}
	if api.echoToken && in.Auth != nil && in.Auth.Token != nil {
		api.reply(w, http.StatusUnprocessableEntity, map[string]string{
			"detail": "Presidio health check failed: upstream said Authorization: Bearer " + *in.Auth.Token,
		})
		return
	}
	connection := api.connections[id]
	switch {
	case r.Method == http.MethodGet && id == "":
		page := map[string]any{"connections": []any{}, "next_cursor": nil}
		for _, c := range api.connections {
			if c.Name == r.URL.Query().Get("name") {
				page["connections"] = []any{c}
			}
		}
		api.reply(w, http.StatusOK, page)
	case r.Method == http.MethodPost && id == "":
		if in.Provider != "presidio" {
			t.Errorf("connection create sent provider %q", in.Provider)
		}
		if in.Auth == nil || (in.Auth.Type == "bearer") != (in.Auth.Token != nil) {
			api.reply(w, http.StatusUnprocessableEntity, map[string]string{"detail": "auth.token is required for bearer"})
			return
		}
		created := &logclient.GatewayGuardrailConnectionRead{
			ID: api.newID(), Name: *in.Name, Provider: "presidio", BaseURL: *in.BaseURL,
			Auth:      logclient.GatewayGuardrailConnectionAuthRead{Type: in.Auth.Type},
			TimeoutMS: *in.TimeoutMS, Enabled: *in.Enabled, CreatedAt: guardrailTestCreatedAt, UpdatedAt: guardrailTestCreatedAt,
		}
		api.connections[created.ID] = created
		if in.Auth.Token != nil {
			api.tokens[created.ID] = *in.Auth.Token
		}
		api.reply(w, http.StatusCreated, created)
	case connection == nil && r.Method != http.MethodDelete:
		api.reply(w, http.StatusNotFound, map[string]string{"detail": "Guardrail connection not found in this organization."})
	case r.Method == http.MethodGet:
		api.reply(w, http.StatusOK, api.withGuardrailCount(connection))
	case r.Method == http.MethodPatch:
		if in.BaseURL != nil && *in.BaseURL != connection.BaseURL && (in.Auth == nil || in.Auth.Token == nil) &&
			connection.Auth.Type == "bearer" && (in.Auth == nil || in.Auth.Type == "bearer") {
			api.reply(w, http.StatusUnprocessableEntity, map[string]string{"detail": "`auth.token` is required when `base_url` changes"})
			return
		}
		if in.Name != nil {
			connection.Name = *in.Name
		}
		if in.BaseURL != nil {
			connection.BaseURL = *in.BaseURL
		}
		if in.Auth != nil {
			connection.Auth.Type = in.Auth.Type
			if in.Auth.Token != nil {
				api.tokens[id] = *in.Auth.Token
			}
			if in.Auth.Type == "none" {
				delete(api.tokens, id)
			}
		}
		if in.TimeoutMS != nil {
			connection.TimeoutMS = *in.TimeoutMS
		}
		if in.Enabled != nil {
			connection.Enabled = *in.Enabled
		}
		api.reply(w, http.StatusOK, api.withGuardrailCount(connection))
	case r.Method == http.MethodDelete:
		for _, guardrail := range api.presidio {
			if guardrail.ConnectionID == id {
				api.reply(w, http.StatusConflict, map[string]string{
					"detail": "A Presidio guardrail uses this guardrail connection. Delete those guardrails first.",
				})
				return
			}
		}
		delete(api.connections, id)
		delete(api.tokens, id)
		api.reply(w, http.StatusNoContent, nil)
	default:
		t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
		api.reply(w, http.StatusMethodNotAllowed, nil)
	}
}

func (api *guardrailAPI) withGuardrailCount(c *logclient.GatewayGuardrailConnectionRead) *logclient.GatewayGuardrailConnectionRead {
	out := *c
	for _, guardrail := range api.presidio {
		if guardrail.ConnectionID == c.ID {
			out.GuardrailCount++
		}
	}
	return &out
}

func (api *guardrailAPI) servePrebuilt(t *testing.T, w http.ResponseWriter, r *http.Request, raw json.RawMessage) {
	slug := itemID(r.URL.Path, prebuiltGuardrailsURL)
	category, known := guardrailTemplates[slug]
	if !known {
		api.reply(w, http.StatusNotFound, map[string]string{"detail": "No guardrail template '" + slug + "'."})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if api.prebuilt[slug] == nil {
			api.reply(w, http.StatusNotFound, map[string]string{
				"detail": "The prebuilt guardrail '" + slug + "' is not configured in this organization.",
			})
			return
		}
	case http.MethodPut:
		var in logclient.GatewayPrebuiltGuardrailSave
		_ = json.Unmarshal(raw, &in)
		if in.SampleMessages != nil {
			api.prebuiltSamples[slug] = *in.SampleMessages
		}
		api.prebuilt[slug] = &logclient.GatewayPrebuiltGuardrailRead{
			TemplateSlug: slug, Name: "Template " + slug, Description: "Detects " + slug + ".", Category: category,
			Targets: in.Targets,
		}
	case http.MethodDelete:
		delete(api.prebuilt, slug)
		api.reply(w, http.StatusNoContent, nil)
		return
	default:
		t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
		api.reply(w, http.StatusMethodNotAllowed, nil)
		return
	}
	out := *api.prebuilt[slug]
	out.SampleMessages = append([]logclient.GatewayGuardrailSampleMessage{}, api.prebuiltSamples[slug]...)
	api.reply(w, http.StatusOK, out)
}

func (api *guardrailAPI) serveRegex(t *testing.T, w http.ResponseWriter, r *http.Request, raw json.RawMessage) {
	id := itemID(r.URL.Path, regexGuardrailsURL)
	var in logclient.GatewayRegexGuardrailWrite
	if raw != nil {
		_ = json.Unmarshal(raw, &in)
	}
	guardrail := api.regex[id]
	switch {
	case r.Method == http.MethodGet && id == "":
		page := map[string]any{"guardrails": []any{}, "next_cursor": nil}
		for _, g := range api.regex {
			if g.Name == r.URL.Query().Get("name") {
				page["guardrails"] = []any{g}
			}
		}
		api.reply(w, http.StatusOK, page)
	case r.Method == http.MethodPost && id == "":
		created := &logclient.GatewayRegexGuardrailRead{
			ID: api.newID(), SampleMessages: []logclient.GatewayGuardrailSampleMessage{},
			CreatedAt: guardrailTestCreatedAt, UpdatedAt: guardrailTestCreatedAt,
		}
		applyRegexWrite(created, in)
		api.regex[created.ID] = created
		api.reply(w, http.StatusCreated, created)
	case guardrail == nil && r.Method != http.MethodDelete:
		api.reply(w, http.StatusNotFound, map[string]string{"detail": "Regex guardrail not found in this organization."})
	case r.Method == http.MethodGet:
		api.reply(w, http.StatusOK, guardrail)
	case r.Method == http.MethodPut:
		applyRegexWrite(guardrail, in)
		api.reply(w, http.StatusOK, guardrail)
	case r.Method == http.MethodDelete:
		delete(api.regex, id)
		api.reply(w, http.StatusNoContent, nil)
	default:
		t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
		api.reply(w, http.StatusMethodNotAllowed, nil)
	}
}

func (api *guardrailAPI) servePresidio(t *testing.T, w http.ResponseWriter, r *http.Request, raw json.RawMessage) {
	id := itemID(r.URL.Path, presidioGuardrailsURL)
	var in logclient.GatewayPresidioGuardrailWrite
	if raw != nil {
		_ = json.Unmarshal(raw, &in)
		if api.connections[in.ConnectionID] == nil {
			api.reply(w, http.StatusUnprocessableEntity, map[string]string{
				"detail": "`connection_id` must name a guardrail connection of this organization.",
			})
			return
		}
	}
	guardrail := api.presidio[id]
	switch {
	case r.Method == http.MethodGet && id == "":
		page := map[string]any{"guardrails": []any{}, "next_cursor": nil}
		for _, g := range api.presidio {
			if g.Name == r.URL.Query().Get("name") {
				page["guardrails"] = []any{g}
			}
		}
		api.reply(w, http.StatusOK, page)
	case r.Method == http.MethodPost && id == "":
		created := &logclient.GatewayPresidioGuardrailRead{
			ID: api.newID(), SampleTexts: []string{}, Status: "active",
			CreatedAt: guardrailTestCreatedAt, UpdatedAt: guardrailTestCreatedAt,
		}
		applyPresidioWrite(created, in)
		api.presidio[created.ID] = created
		api.reply(w, http.StatusCreated, created)
	case guardrail == nil && r.Method != http.MethodDelete:
		api.reply(w, http.StatusNotFound, map[string]string{"detail": "Presidio guardrail not found in this organization."})
	case r.Method == http.MethodGet:
		api.reply(w, http.StatusOK, guardrail)
	case r.Method == http.MethodPut:
		applyPresidioWrite(guardrail, in)
		api.reply(w, http.StatusOK, guardrail)
	case r.Method == http.MethodDelete:
		delete(api.presidio, id)
		api.reply(w, http.StatusNoContent, nil)
	default:
		t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
		api.reply(w, http.StatusMethodNotAllowed, nil)
	}
}

// applyRegexWrite stores a regex guardrail write. Omitted samples stay unchanged.
func applyRegexWrite(g *logclient.GatewayRegexGuardrailRead, in logclient.GatewayRegexGuardrailWrite) {
	g.Name, g.Description, g.Pattern, g.Flags, g.Targets = in.Name, in.Description, in.Pattern, in.Flags, in.Targets
	if in.SampleMessages != nil {
		g.SampleMessages = *in.SampleMessages
	}
}

// applyPresidioWrite stores a Presidio guardrail write. Omitted samples stay unchanged.
func applyPresidioWrite(g *logclient.GatewayPresidioGuardrailRead, in logclient.GatewayPresidioGuardrailWrite) {
	g.Name, g.Description, g.ConnectionID, g.OnError = in.Name, in.Description, in.ConnectionID, in.OnError
	g.EntityType, g.Language, g.Operator, g.ScoreThreshold = in.EntityType, in.Language, in.Operator, in.ScoreThreshold
	g.Targets = in.Targets
	if in.SampleTexts != nil {
		g.SampleTexts = *in.SampleTexts
	}
}

// unknownEndpoint reports whether a write body targets the endpoint that the fake API does not have.
func unknownEndpoint(raw json.RawMessage) bool {
	var body struct {
		Targets []logclient.GatewayGuardrailTarget `json:"targets"`
	}
	_ = json.Unmarshal(raw, &body)
	for _, target := range body.Targets {
		if target.Target.EndpointSlug == unknownEndpointSlug {
			return true
		}
	}
	return false
}

// deniedAction returns the first target action of a write body that is not observe.
func deniedAction(raw json.RawMessage) string {
	var body struct {
		Targets []logclient.GatewayGuardrailTarget `json:"targets"`
	}
	_ = json.Unmarshal(raw, &body)
	for _, target := range body.Targets {
		if target.Action != "observe" {
			return target.Action
		}
	}
	return ""
}
