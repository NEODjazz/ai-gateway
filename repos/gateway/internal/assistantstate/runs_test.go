package assistantstate

import "testing"

func TestRunTransitionGraph(t *testing.T) {
	valid := map[string][]string{
		"queued":          {"in_progress", "cancelling", "cancelled", "failed", "expired"},
		"in_progress":     {"requires_action", "completed", "failed", "cancelling", "cancelled", "expired", "incomplete"},
		"requires_action": {"queued", "in_progress", "cancelling", "cancelled", "failed", "expired"},
		"cancelling":      {"cancelled", "failed"},
	}
	statuses := []string{"queued", "in_progress", "requires_action", "cancelling", "completed", "failed", "cancelled", "expired", "incomplete"}
	for _, from := range statuses {
		allowed := map[string]bool{}
		for _, to := range valid[from] {
			allowed[to] = true
		}
		for _, to := range statuses {
			if got := ValidRunTransition(from, to); got != allowed[to] {
				t.Fatalf("transition %s -> %s = %v, want %v", from, to, got, allowed[to])
			}
		}
	}
}

func TestRunStepTransitionGraph(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled", "expired"} {
		if !ValidRunStepTransition("in_progress", status) {
			t.Fatalf("in_progress -> %s rejected", status)
		}
		if ValidRunStepTransition(status, "completed") {
			t.Fatalf("terminal transition from %s accepted", status)
		}
	}
	if ValidRunStepTransition("in_progress", "in_progress") || ValidRunStepTransition("in_progress", "unknown") {
		t.Fatal("invalid run-step transition accepted")
	}
}
