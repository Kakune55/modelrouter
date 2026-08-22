package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEncodeInfluxLine(t *testing.T) {
	completedAt := time.Unix(1_700_000_000, 123)
	ev := Event{
		Client:             "client a",
		Model:              "provider/model,1",
		RouteGroup:         "group=primary",
		Endpoint:           "endpoint-a",
		StatusCode:         200,
		Duration:           2500 * time.Millisecond,
		BytesOut:           1024,
		PromptTokens:       10,
		OutputTokens:       20,
		TotalTokens:        30,
		Streaming:          true,
		TTFT:               125 * time.Millisecond,
		GenerationDuration: 2 * time.Second,
	}

	got, err := EncodeInfluxLine(ev, map[string]string{
		"environment": "prod east",
		"instance":    "router,1",
	}, completedAt)
	if err != nil {
		t.Fatalf("EncodeInfluxLine() error = %v", err)
	}
	want := "modelrouter_request,client=client\\ a,endpoint=endpoint-a,environment=prod\\ east,instance=router\\,1,model=provider/model\\,1,route_group=group\\=primary,status_code=200 requests=1i,duration_ms=2500,bytes_out=1024i,prompt_tokens=10i,output_tokens=20i,total_tokens=30i,streaming=true,success=true,ttft_ms=125,end_to_end_token_rate=8,generation_token_rate=10 1700000000000000123"
	if got != want {
		t.Fatalf("line protocol mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestEncodeInfluxLineOmitsUnavailableDerivedFields(t *testing.T) {
	got, err := EncodeInfluxLine(Event{StatusCode: 503, Err: errors.New("upstream token must not leak")}, nil, time.Unix(10, 0))
	if err != nil {
		t.Fatalf("EncodeInfluxLine() error = %v", err)
	}
	want := "modelrouter_request,status_code=503 requests=1i,duration_ms=0,bytes_out=0i,prompt_tokens=0i,output_tokens=0i,total_tokens=0i,streaming=false,success=false 10000000000"
	if got != want {
		t.Fatalf("line protocol mismatch\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "upstream token") {
		t.Fatalf("line protocol leaked error: %s", got)
	}
}

func TestEncodeInfluxLineBuiltInTagsTakePrecedence(t *testing.T) {
	got, err := EncodeInfluxLine(Event{Client: "actual-client", StatusCode: 204}, map[string]string{
		"client":      "configured-client",
		"status_code": "500",
	}, time.Unix(10, 0))
	if err != nil {
		t.Fatalf("EncodeInfluxLine() error = %v", err)
	}
	if !strings.Contains(got, ",client=actual-client,") || !strings.Contains(got, ",status_code=204 ") {
		t.Fatalf("built-in tags did not take precedence: %s", got)
	}
	if strings.Contains(got, "configured-client") || strings.Contains(got, "status_code=500") {
		t.Fatalf("line protocol retained overridden tags: %s", got)
	}
}

func TestEncodeInfluxLineRejectsNewlinesInTags(t *testing.T) {
	for _, tags := range []map[string]string{
		{"bad\nkey": "value"},
		{"key": "bad\rvalue"},
	} {
		if _, err := EncodeInfluxLine(Event{StatusCode: 200}, tags, time.Now()); !errors.Is(err, errInfluxTagContainsNewline) {
			t.Fatalf("EncodeInfluxLine() error = %v", err)
		}
	}
}
