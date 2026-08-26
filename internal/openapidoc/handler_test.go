package openapidoc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerServesDocument(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rr := httptest.NewRecorder()

	Handler()(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %s", got)
	}
	var doc struct {
		OpenAPI    string                     `json:"openapi"`
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %s", doc.OpenAPI)
	}
	for _, schema := range []string{"InfluxDBConfig", "InfluxDBExporterStatus", "ClientKeyIssueRequest", "ClientKeyIssueResponse"} {
		if _, ok := doc.Components.Schemas[schema]; !ok {
			t.Fatalf("missing schema %q", schema)
		}
	}
	var clientKeysPath struct {
		Post struct {
			OperationID string `json:"operationId"`
		} `json:"post"`
	}
	if err := json.Unmarshal(doc.Paths["/admin/client-keys"], &clientKeysPath); err != nil {
		t.Fatalf("decode client key path: %v", err)
	}
	if got := clientKeysPath.Post.OperationID; got != "issueAdminClientKey" {
		t.Fatalf("client key issue operationId = %q", got)
	}
}

func TestHandlerRejectsUnsupportedMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/openapi.json", nil)
	rr := httptest.NewRecorder()

	Handler()(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}
