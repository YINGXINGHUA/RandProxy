package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestControlPlaneAPIOverview_returnsEmptySourcesArray_whenNoProvidersConfigured(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	handler := srv.webHandler()

	// When
	recorder := performLoopbackJSONRequest(t, handler, http.MethodGet, "/api/v1/overview", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("overview status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Then
	body := decodeJSONResponse[struct {
		Overview map[string]json.RawMessage `json:"overview"`
	}](t, recorder)

	sources, ok := body.Overview["sources"]
	if !ok {
		t.Fatalf("overview missing sources: %#v", body.Overview)
	}
	if string(sources) != "[]" {
		t.Fatalf("overview sources = %s, want []", sources)
	}
}
