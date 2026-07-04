package game

type taskStateStep struct {
	State  ObjectiveState
	Role   string
	Reason string
}

type taskStateMachine struct {
	state            ObjectiveState
	reviewMode       bool
	reviewTarget     int
	reviewsCompleted int
	// proverDelivered records whether at least one prover turn completed
	// without error. Review mode ends after a fixed number of cycles
	// regardless of the verifier's verdict, so without this guard a run whose
	// every implementation turn failed (e.g. tool-loop/protocol errors with no
	// artifact produced) would still be reported as success.
	proverDelivered bool
}

func newTaskStateMachine(reviewTarget int) taskStateMachine {
	return taskStateMachine{
		state:        ObjectiveStatePlan,
		reviewMode:   reviewTarget > 0,
		reviewTarget: reviewTarget,
	}
}

func (m taskStateMachine) next() (taskStateStep, bool) {
	switch m.state {
	case ObjectiveStatePlan, ObjectiveStateImplement:
		return taskStateStep{State: ObjectiveStateImplement, Role: RoleProver, Reason: "starting_implement_turn"}, true
	case ObjectiveStateRepair:
		return taskStateStep{State: ObjectiveStateRepair, Role: RoleProver, Reason: "starting_repair_turn"}, true
	case ObjectiveStateVerify:
		return taskStateStep{State: ObjectiveStateVerify, Role: RoleVerifier, Reason: "starting_verify_turn"}, true
	default:
		return taskStateStep{}, false
	}
}

func (m taskStateMachine) currentState() ObjectiveState {
	return m.state
}

func (m *taskStateMachine) observe(step taskStateStep, turn TurnResult) (GameResult, bool) {
	if turn.Error != "" {
		return m.fail(step, turn.Recoverable)
	}
	return m.advance(turn)
}

func (m *taskStateMachine) fail(step taskStateStep, recoverable bool) (GameResult, bool) {
	if recoverable {
		m.state = step.State
		return "", false
	}
	m.state = ObjectiveStateBlocked
	return GameFailure, true
}

func (m *taskStateMachine) advance(turn TurnResult) (GameResult, bool) {
	if turn.Error != "" {
		return m.fail(taskStateStep{State: m.state}, turn.Recoverable)
	}

	switch turn.Role {
	case RoleProver:
		m.proverDelivered = true
		m.state = ObjectiveStateVerify
	case RoleVerifier:
		if m.reviewMode {
			m.reviewsCompleted++
			if m.reviewsCompleted >= m.reviewTarget {
				if !m.proverDelivered {
					m.blockNoProverDelivery()
					return GameFailure, true
				}
				m.state = ObjectiveStateFinalize
				return GameSuccess, true
			}
			m.state = ObjectiveStateImplement
			return "", false
		}
		if turn.Error == "" && turn.Status == StatusConcede {
			if !m.proverDelivered {
				m.blockNoProverDelivery()
				return GameFailure, true
			}
			m.state = ObjectiveStateFinalize
			return GameSuccess, true
		}
		m.state = ObjectiveStateRepair
	}
	return "", false
}

func (m *taskStateMachine) blockNoProverDelivery() {
	m.state = ObjectiveStateBlocked
}
