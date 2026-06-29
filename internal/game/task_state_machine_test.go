package game

import "testing"

func TestTaskStateMachineDefaultLifecycle(t *testing.T) {
	machine := newTaskStateMachine(0)

	step, ok := machine.next()
	if !ok || step.Role != "prover" || step.State != ObjectiveStateImplement {
		t.Fatalf("first step: %#v ok=%v", step, ok)
	}
	if result, terminal := machine.advance(TurnResult{Role: "prover", Status: StatusContinue}); terminal || result != "" {
		t.Fatalf("prover should continue to verify: result=%q terminal=%v", result, terminal)
	}

	step, ok = machine.next()
	if !ok || step.Role != "verifier" || step.State != ObjectiveStateVerify {
		t.Fatalf("verify step: %#v ok=%v", step, ok)
	}
	if result, terminal := machine.advance(TurnResult{Role: "verifier", Status: StatusContinue}); terminal || result != "" {
		t.Fatalf("continuing verifier should enter repair: result=%q terminal=%v", result, terminal)
	}

	step, ok = machine.next()
	if !ok || step.Role != "prover" || step.State != ObjectiveStateRepair {
		t.Fatalf("repair step: %#v ok=%v", step, ok)
	}
	if result, terminal := machine.advance(TurnResult{Role: "prover", Status: StatusContinue}); terminal || result != "" {
		t.Fatalf("repair prover should continue to verify: result=%q terminal=%v", result, terminal)
	}

	step, ok = machine.next()
	if !ok || step.Role != "verifier" || step.State != ObjectiveStateVerify {
		t.Fatalf("second verify step: %#v ok=%v", step, ok)
	}
	result, terminal := machine.advance(TurnResult{Role: "verifier", Status: StatusConcede})
	if !terminal || result != GameSuccess || machine.state != ObjectiveStateFinalize {
		t.Fatalf("concede should finalize: result=%q terminal=%v state=%q", result, terminal, machine.state)
	}
	if _, ok := machine.next(); ok {
		t.Fatal("finalized machine should not produce more turns")
	}
}

func TestTaskStateMachineBlocksOnNonrecoverableError(t *testing.T) {
	machine := newTaskStateMachine(0)
	result, terminal := machine.advance(TurnResult{
		Role:   "prover",
		Status: StatusContinue,
		Error:  "provider_invalid_request: bad model",
	})
	if !terminal || result != GameFailure || machine.state != ObjectiveStateBlocked {
		t.Fatalf("nonrecoverable error should block: result=%q terminal=%v state=%q", result, terminal, machine.state)
	}
}

func TestTaskStateMachineReviewModeCountsOnlySuccessfulVerifierTurns(t *testing.T) {
	machine := newTaskStateMachine(2)

	machine.advance(TurnResult{Role: "prover", Status: StatusContinue})
	result, terminal := machine.advance(TurnResult{
		Role:        "verifier",
		Status:      StatusContinue,
		Error:       "provider_unavailable: retry",
		Recoverable: true,
	})
	if terminal || result != "" || machine.reviewsCompleted != 0 || machine.state != ObjectiveStateImplement {
		t.Fatalf("failed review should not count: result=%q terminal=%v reviews=%d state=%q", result, terminal, machine.reviewsCompleted, machine.state)
	}

	machine.advance(TurnResult{Role: "prover", Status: StatusContinue})
	result, terminal = machine.advance(TurnResult{Role: "verifier", Status: StatusContinue})
	if terminal || result != "" || machine.reviewsCompleted != 1 || machine.state != ObjectiveStateImplement {
		t.Fatalf("first successful review should continue: result=%q terminal=%v reviews=%d state=%q", result, terminal, machine.reviewsCompleted, machine.state)
	}

	machine.advance(TurnResult{Role: "prover", Status: StatusContinue})
	result, terminal = machine.advance(TurnResult{Role: "verifier", Status: StatusConcede})
	if !terminal || result != GameSuccess || machine.reviewsCompleted != 2 || machine.state != ObjectiveStateFinalize {
		t.Fatalf("second successful review should finalize: result=%q terminal=%v reviews=%d state=%q", result, terminal, machine.reviewsCompleted, machine.state)
	}
}

func TestTaskStateMachineReviewModeFailsWhenProverNeverDelivers(t *testing.T) {
	machine := newTaskStateMachine(1)

	// Every prover turn errors (recoverably) so nothing is ever implemented.
	machine.advance(TurnResult{
		Role:        "prover",
		Status:      StatusContinue,
		Error:       "agent_incomplete:tool_loop_exceeded:160",
		Recoverable: true,
	})
	result, terminal := machine.advance(TurnResult{Role: "verifier", Status: StatusContinue})
	if !terminal || result != GameFailure || machine.proverDelivered {
		t.Fatalf("review cycles completing with no successful prover turn should fail: result=%q terminal=%v delivered=%v", result, terminal, machine.proverDelivered)
	}
}

func TestTaskStateMachineReviewModeSucceedsWhenProverDelivers(t *testing.T) {
	machine := newTaskStateMachine(1)

	machine.advance(TurnResult{Role: "prover", Status: StatusContinue})
	result, terminal := machine.advance(TurnResult{Role: "verifier", Status: StatusContinue})
	if !terminal || result != GameSuccess || !machine.proverDelivered {
		t.Fatalf("clean prover + completed review should succeed: result=%q terminal=%v delivered=%v", result, terminal, machine.proverDelivered)
	}
}

func TestTaskStateMachineNormalModeFailsWhenVerifierConcedesWithoutProverDelivery(t *testing.T) {
	machine := newTaskStateMachine(0)

	machine.advance(TurnResult{
		Role:        "prover",
		Status:      StatusContinue,
		Error:       "provider_rate_limited: retry later",
		Recoverable: true,
	})
	result, terminal := machine.advance(TurnResult{Role: "verifier", Status: StatusConcede})
	if !terminal || result != GameFailure || machine.state != ObjectiveStateBlocked {
		t.Fatalf("verifier concession without delivered prover should fail: result=%q terminal=%v state=%q", result, terminal, machine.state)
	}
}
