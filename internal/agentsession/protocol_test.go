package agentsession

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCompactionPayloadRoundTrip(t *testing.T) {
	want := CompactionPayload{
		Reason:          "token_budget",
		FirstKeptIndex:  42,
		TokensBefore:    50000,
		TokensAfter:     18000,
		SummaryTokens:   1200,
		ItemsSummarized: 41,
		ItemsKept:       9,
		Model:           "test/model",
		ResponseID:      "resp_compact",
		Usage:           ModelResponseUsage{Input: 1000, Output: 200, CacheRead: 500},
		Details:         CompactionDetails{ReadFiles: []string{"internal/executor/responses.go"}},
	}

	event := Event{Type: KindCompaction, Data: MarshalPayload(&want)}
	got, err := Unmarshal[CompactionPayload](&event)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip: got %#v want %#v", *got, want)
	}
}

func TestModelAsyncJobPayloadRoundTrip(t *testing.T) {
	want := ModelAsyncJobPayload{
		Sequence:  3,
		JobID:     "job_123",
		Provider:  "bifrost",
		Model:     "test/model",
		Transport: "bifrost_async",
		Status:    "processing",
	}

	event := Event{Type: KindModelAsyncJob, Data: MarshalPayload(&want)}
	got, err := Unmarshal[ModelAsyncJobPayload](&event)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip: got %#v want %#v", *got, want)
	}
}

func TestProtocolGoldenFixtures(t *testing.T) {
	fixture, err := os.ReadFile("testdata/protocol_events.golden.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	scanner := bufio.NewScanner(bytes.NewReader(fixture))
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		if event.SchemaVersion != SchemaVersion {
			t.Fatalf("schema_version: got %d want %d", event.SchemaVersion, SchemaVersion)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("events: got %d", len(events))
	}
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(fixture, append(data, '\n')) {
			t.Fatalf("golden fixture changed or missing line:\n%s", data)
		}
	}
}

func TestProtocolUnknownFieldTolerance(t *testing.T) {
	raw := []byte(`{"schema":"telos.agent_session.v1","schema_version":1,"type":"terminal","version":1,"future":"ok","data":{"terminal_state":"completed","new_field":true}}`)
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	payload, err := Unmarshal[TerminalPayload](&event)
	if err != nil {
		t.Fatal(err)
	}
	if payload.TerminalState != TerminalCompleted {
		t.Fatalf("terminal_state: got %q", payload.TerminalState)
	}
}
