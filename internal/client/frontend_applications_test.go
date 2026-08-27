// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFrontendApplicationLifecycleRequests(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/" {
				t.Fatalf("create request = %s %s", r.Method, r.URL.EscapedPath())
			}
			var in FrontendApplicationCreate
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatal(err)
			}
			if in.Name != "browser" || in.Environment == nil || *in.Environment != "production" || !in.AdoptExistingServiceName {
				t.Fatalf("unexpected create input: %+v", in)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"app-id","project_id":"project-id","name":"browser","service_namespace":null,"environment":"production","created_at":"2026-01-01T00:00:00Z","token_id":"token-1","token":"secret-1"}`))
		case 2:
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/app-id/" {
				t.Fatalf("get request = %s %s", r.Method, r.URL.EscapedPath())
			}
			_, _ = w.Write([]byte(`{"id":"app-id","project_id":"project-id","name":"browser","service_namespace":null,"environment":"production","created_at":"2026-01-01T00:00:00Z"}`))
		case 3:
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/app-id/tokens/" {
				t.Fatalf("token create request = %s %s", r.Method, r.URL.EscapedPath())
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token_id":"token-2","description":"Issued to replace the previous token","status":"active","token":"secret-2","created_at":"2026-01-02T00:00:00Z","expires_at":null,"last_used_at":null}`))
		case 4:
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/app-id/tokens/" {
				t.Fatalf("token list request = %s %s", r.Method, r.URL.EscapedPath())
			}
			if r.URL.Query().Get("limit") != "100" || r.URL.Query().Has("cursor") {
				t.Fatalf("unexpected token list query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"token-2","description":null,"status":"active","token":"secret-2","created_at":"2026-01-02T00:00:00Z","expires_at":null,"last_used_at":null}],"next_cursor":null}`))
		case 5:
			if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/app-id/tokens/token-2/revoke/" {
				t.Fatalf("revoke request = %s %s", r.Method, r.URL.EscapedPath())
			}
			w.WriteHeader(http.StatusNoContent)
		case 6:
			if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/app-id/" {
				t.Fatalf("delete request = %s %s", r.Method, r.URL.EscapedPath())
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, r.Method, r.URL.EscapedPath())
		}
	}))
	defer server.Close()

	client, err := NewAPIClient(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	environment := "production"
	created, err := client.CreateFrontendApplication(context.Background(), "project-id", FrontendApplicationCreate{Name: "browser", Environment: &environment, AdoptExistingServiceName: true})
	if err != nil || created.Token != "secret-1" {
		t.Fatalf("create: out=%+v err=%v", created, err)
	}
	application, status, err := client.GetFrontendApplication(context.Background(), "project-id", "app-id")
	if err != nil || status != http.StatusOK || application.ID != "app-id" {
		t.Fatalf("get: out=%+v status=%d err=%v", application, status, err)
	}
	token, err := client.CreateFrontendApplicationToken(context.Background(), "project-id", "app-id")
	if err != nil || token.TokenID != "token-2" || token.Status != "active" || token.CreatedAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("create token: out=%+v err=%v", token, err)
	}
	tokens, err := client.ListFrontendApplicationTokens(context.Background(), "project-id", "app-id")
	if err != nil || len(tokens) != 1 || tokens[0].Token == nil || *tokens[0].Token != "secret-2" {
		t.Fatalf("list tokens: out=%+v err=%v", tokens, err)
	}
	if err := client.RevokeFrontendApplicationToken(context.Background(), "project-id", "app-id", "token-2"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteFrontendApplication(context.Background(), "project-id", "app-id"); err != nil {
		t.Fatal(err)
	}
}

func TestGetFrontendApplicationNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	client, err := NewAPIClient(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	application, status, err := client.GetFrontendApplication(context.Background(), "project", "missing")
	if err != nil || status != http.StatusNotFound || application != nil {
		t.Fatalf("out=%+v status=%d err=%v", application, status, err)
	}
}

func TestListFrontendApplications(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/project-id/frontend-applications/" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Fatalf("unexpected limit: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("cursor") == "next page" {
			_, _ = w.Write([]byte(`{"data":[{"id":"app-2","project_id":"project-id","name":"admin","service_namespace":null,"environment":null,"created_at":"2026-01-02T00:00:00Z"}],"next_cursor":null}`))
			return
		}
		if r.URL.Query().Has("cursor") {
			t.Fatalf("unexpected cursor: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"app-id","project_id":"project-id","name":"browser","service_namespace":null,"environment":null,"created_at":"2026-01-01T00:00:00Z"}],"next_cursor":"next page"}`))
	}))
	defer server.Close()
	client, err := NewAPIClient(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	applications, err := client.ListFrontendApplications(context.Background(), "project-id")
	if err != nil || len(applications) != 2 || applications[0].Name != "browser" || applications[1].Name != "admin" {
		t.Fatalf("out=%+v err=%v", applications, err)
	}
}

func TestListFrontendApplicationTokens(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "100" {
			t.Fatalf("unexpected limit: %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("cursor") == "token cursor" {
			_, _ = w.Write([]byte(`{"data":[{"id":"active","description":null,"status":"active","token":"secret","created_at":"2026-01-02T00:00:00Z","expires_at":null,"last_used_at":null}],"next_cursor":null}`))
			return
		}
		if r.URL.Query().Has("cursor") {
			t.Fatalf("unexpected cursor: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"revoked","description":null,"status":"revoked","token":null,"created_at":"2026-01-01T00:00:00Z","expires_at":null,"last_used_at":null}],"next_cursor":"token cursor"}`))
	}))
	defer server.Close()
	client, err := NewAPIClient(server.URL, "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := client.ListFrontendApplicationTokens(context.Background(), "project-id", "app-id")
	if err != nil || len(tokens) != 2 || tokens[1].ID != "active" || tokens[1].Token == nil || *tokens[1].Token != "secret" {
		t.Fatalf("out=%+v err=%v", tokens, err)
	}
}
