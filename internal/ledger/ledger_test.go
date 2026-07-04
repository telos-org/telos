package ledger

import (
	"path/filepath"
	"testing"

	"github.com/telos-org/telos/internal/protocol"
)

func TestInitializeReadWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objective-ledger.json")
	initial := New("sess-1", "system", "Do the thing.\n\nMore detail.")
	if err := Initialize(path, initial); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != Schema || got.Objective != "Do the thing." || got.State != ObjectiveStatePlan {
		t.Fatalf("ledger: %+v", got)
	}
	got.Turns = append(got.Turns, ObjectiveTurn{RoundNum: 1, Role: protocol.RoleProver, Status: protocol.StatusContinue})
	if err := Write(path, got); err != nil {
		t.Fatal(err)
	}
	again, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Turns) != 1 || again.Turns[0].Status != protocol.StatusContinue {
		t.Fatalf("turns: %+v", again.Turns)
	}
}
