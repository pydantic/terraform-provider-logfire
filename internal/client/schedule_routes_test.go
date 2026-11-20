// Copyright Pydantic, Inc. 2025, 2026
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

const (
	notificationWindowsRoute = "/api/v1/notification-windows/"
	legacySchedulesRoute     = "/api/v1/schedules/"
)

// scheduleRoutesTransport serves the schedule routes under the prefixes in
// serves, answers any other path as FastAPI answers a missing route, and
// records each "METHOD path" it receives. The schedule "missing" does not exist.
type scheduleRoutesTransport struct {
	serves []string
	calls  *[]string
}

func (t scheduleRoutesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*t.calls = append(*t.calls, req.Method+" "+req.URL.Path)
	status, body := http.StatusNotFound, `{"detail":"Not Found"}`
	if slices.ContainsFunc(t.serves, func(prefix string) bool { return strings.HasPrefix(req.URL.Path, prefix) }) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/missing/"):
			body = `{"detail":"No notification window with that ID in this organization."}`
		case req.Method == http.MethodPost:
			status, body = http.StatusCreated, `{"id":"s1"}`
		case req.Method == http.MethodDelete:
			status, body = http.StatusNoContent, ""
		default:
			status, body = http.StatusOK, `{"id":"s1"}`
		}
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func TestScheduleRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		serves []string
		want   []string
	}{
		{
			name:   "current instance uses notification windows",
			serves: []string{notificationWindowsRoute},
			want: []string{
				"POST " + notificationWindowsRoute,
				"GET " + notificationWindowsRoute + "s1/",
				"PUT " + notificationWindowsRoute + "s1/",
				"DELETE " + notificationWindowsRoute + "s1/",
			},
		},
		{
			name:   "older instance falls back once and then skips the missing route",
			serves: []string{legacySchedulesRoute},
			want: []string{
				"POST " + notificationWindowsRoute,
				"POST " + legacySchedulesRoute,
				"GET " + legacySchedulesRoute + "s1/",
				"PUT " + legacySchedulesRoute + "s1/",
				"DELETE " + legacySchedulesRoute + "s1/",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls []string
			c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: scheduleRoutesTransport{serves: tc.serves, calls: &calls}})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := c.CreateSchedule(ctx, ScheduleCreate{Label: "office hours", Timezone: "UTC"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.GetSchedule(ctx, "s1"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.UpdateSchedule(ctx, "s1", ScheduleUpdate{}); err != nil {
				t.Fatal(err)
			}
			if err := c.DeleteSchedule(ctx, "s1"); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, tc.want) {
				t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestScheduleMissingIDIsNotFound(t *testing.T) {
	t.Parallel()
	for _, serves := range [][]string{
		{notificationWindowsRoute},
		{legacySchedulesRoute},
		{notificationWindowsRoute, legacySchedulesRoute},
	} {
		var calls []string
		c, err := NewAPIClient("https://example.invalid", "test-token", &http.Client{Transport: scheduleRoutesTransport{serves: serves, calls: &calls}})
		if err != nil {
			t.Fatal(err)
		}
		if _, status, err := c.GetSchedule(context.Background(), "missing"); !IsNotFoundError(err) || status != http.StatusNotFound {
			t.Fatalf("serves %v: status %d, error %v; want a not-found error", serves, status, err)
		}
		// The window's own 404 is final, so a failure on the old route cannot replace it.
		if serves[0] == notificationWindowsRoute && len(calls) != 1 {
			t.Fatalf("serves %v: requests %v; want only the notification windows route", serves, calls)
		}
	}
}
