package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The live route must deny every swap operation without needing a Manager,
// network client, restart owner, staged artifact or SSE subscriber.
func TestDistributionUpdateRoutesDisabled(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		method, path string
		handle       http.HandlerFunc
	}{
		{"GET", "/api/update/check?force=1", s.updateCheck},
		{"POST", "/api/update/apply", s.updateApply},
		{"POST", "/api/update/rollback", s.updateRollback},
		{"GET", "/api/update/stream", s.updateStream},
	} {
		t.Run(tc.path, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("disabled route accessed runtime/update dependencies: %v", p)
				}
			}()
			w := httptest.NewRecorder()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			tc.handle(w, httptest.NewRequest(tc.method, tc.path, nil).WithContext(ctx))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "disabled") {
				t.Fatalf("missing disabled explanation: %s", w.Body.String())
			}
			if strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
				t.Fatal("disabled update admitted SSE")
			}
		})
	}
}
