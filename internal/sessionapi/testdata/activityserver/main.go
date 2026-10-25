// A real Sessions API fixture for the coordinated cloud/runtime contract check.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos/internal/sessionapi"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	root := os.Args[1]
	for session, count := range map[string]int{"contract": 1642, "snapshot": 50} {
		dir := filepath.Join(root, session)
		must(os.MkdirAll(dir, 0o700))
		path := filepath.Join(dir, "evidence.jsonl")
		var evidence strings.Builder
		for seq := 1; seq <= count; seq++ {
			data := map[string]any{"audience": "agent", "kind": "tool", "text": "Repeated update"}
			if seq%2 == 0 {
				delete(data, "audience") // Historical tools predate this field.
			}
			if seq%23 == 0 || session == "snapshot" {
				data["audience"], data["kind"] = "user", "progress_update"
			}
			event := map[string]any{"schema": "telos.evidence.v2", "session_id": session, "event": "agent_progress", "ts": "2026-10-21T00:00:00Z", "data": data}
			if session != "snapshot" {
				event["event_seq"] = seq
			}
			must(json.NewEncoder(&evidence).Encode(event))
		}
		must(os.WriteFile(path, []byte(evidence.String()), 0o600))
		manifest := sessionapi.ManifestFromInitial(sessionapi.InitialManifest{
			SessionID: session, SpecName: "example",
			Specs: []sessionapi.InitialManifestSpec{{Name: "example", DirName: "example", EvidencePath: &path}},
		})
		must(sessionapi.WriteManifest(filepath.Join(dir, "session.json"), &manifest))
	}
	store := sessionapi.NewFileStore(root, sessionapi.RuntimeLocal)
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, store, sessionapi.NewBearerAuthorizer(store, "contract-token"), sessionapi.RuntimeIdentity{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	fmt.Println("http://" + listener.Addr().String())
	must(http.Serve(listener, mux))
}
