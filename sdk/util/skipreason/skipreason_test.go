package skipreason

import "testing"

func TestReasonConstants(t *testing.T) {
	tests := []struct {
		name  string
		got   string
		want  string
	}{
		{"ReasonInProgress", ReasonInProgress, "in_progress"},
		{"ReasonDone", ReasonDone, "done"},
		{"ReasonBlocked", ReasonBlocked, "blocked"},
		{"ReasonNeedsClarification", ReasonNeedsClarification, "needs_clarification"},
		{"ReasonSuperseded", ReasonSuperseded, "superseded"},
		{"ReasonFailedSkip", ReasonFailedSkip, "failed_skip"},
		{"ReasonRetryReadySkip", ReasonRetryReadySkip, "retry_ready_skip"},
		{"ReasonProcessedGrace", ReasonProcessedGrace, "processed_grace"},
		{"ReasonTaskQueued", ReasonTaskQueued, "task_queued"},
		{"ReasonHasMergedWork", ReasonHasMergedWork, "has_merged_work"},
		{"ReasonPendingDependency", ReasonPendingDependency, "pending_dependency"},
		{"ReasonCompletedExecution", ReasonCompletedExecution, "completed_execution"},
		{"ReasonFreshLabelCheck", ReasonFreshLabelCheck, "fresh_label_check"},
		{"ReasonPreFlightReject", ReasonPreFlightReject, "pre_flight_reject"},
		{"ReasonStatusLabel", ReasonStatusLabel, "status_label"},
		{"ReasonStatusTag", ReasonStatusTag, "status_tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestReasonConstantsUnique(t *testing.T) {
	all := []struct {
		name  string
		value string
	}{
		{"ReasonInProgress", ReasonInProgress},
		{"ReasonDone", ReasonDone},
		{"ReasonBlocked", ReasonBlocked},
		{"ReasonNeedsClarification", ReasonNeedsClarification},
		{"ReasonSuperseded", ReasonSuperseded},
		{"ReasonFailedSkip", ReasonFailedSkip},
		{"ReasonRetryReadySkip", ReasonRetryReadySkip},
		{"ReasonProcessedGrace", ReasonProcessedGrace},
		{"ReasonTaskQueued", ReasonTaskQueued},
		{"ReasonHasMergedWork", ReasonHasMergedWork},
		{"ReasonPendingDependency", ReasonPendingDependency},
		{"ReasonCompletedExecution", ReasonCompletedExecution},
		{"ReasonFreshLabelCheck", ReasonFreshLabelCheck},
		{"ReasonPreFlightReject", ReasonPreFlightReject},
		{"ReasonStatusLabel", ReasonStatusLabel},
		{"ReasonStatusTag", ReasonStatusTag},
	}
	seen := make(map[string]string, len(all))
	for _, c := range all {
		if prev, ok := seen[c.value]; ok {
			t.Errorf("duplicate value %q: %s and %s", c.value, prev, c.name)
		}
		seen[c.value] = c.name
	}
}
