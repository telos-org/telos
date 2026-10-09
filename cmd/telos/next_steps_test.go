package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/telos-org/telos/internal/cloud"
)

func TestDescribeTellsTheOwnerHowToMoveAGoalForward(t *testing.T) {
	session := cloud.SessionRecord{
		ID:           "goal_c7d2f0a4e8",
		Name:         "reading-list",
		Status:       "needs_attention",
		StatusReason: "The verifier rejected the latest revision.",
	}
	for _, tt := range []struct {
		followUp string
		want     string
	}{
		{"", "Next      Fix the spec, then: telos apply SPEC.md --goal goal_c7d2f0a4e8, or telos delete goal_c7d2f0a4e8\n"},
		{"@team", "Next      Fix the spec, then: telos apply SPEC.md --goal goal_c7d2f0a4e8 --context @team, or telos delete goal_c7d2f0a4e8 --context @team\n"},
	} {
		var out bytes.Buffer
		printCloudSessionDescriptionForContext(&out, session, "@team", tt.followUp)
		if !strings.HasSuffix(out.String(), tt.want) {
			t.Fatalf("description:\n%s\nwant it to end with:\n%s", out.String(), tt.want)
		}
	}

	session.Status = "ready"
	var out bytes.Buffer
	printCloudSessionDescriptionForContext(&out, session, "personal", "")
	if strings.Contains(out.String(), "Next") {
		t.Fatalf("a ready Goal got a next step:\n%s", out.String())
	}
}

func TestPlanAndApplyNameAMissingSpec(t *testing.T) {
	for _, command := range []string{"plan", "apply"} {
		t.Run(command, func(t *testing.T) {
			process := exec.Command(os.Args[0], "-test.run=^TestInferenceCLIProcess$", "--", command, "missing.md")
			process.Dir = t.TempDir()
			process.Env = append(os.Environ(), "TELOS_TEST_INFERENCE_COMMAND=1", "TELOS_SESSION_ID=")
			out, err := process.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "error: spec file not found: missing.md\n") {
				t.Fatalf("err=%v output=%s", err, out)
			}
		})
	}
}
