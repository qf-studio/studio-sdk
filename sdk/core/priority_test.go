package core

import "testing"

func TestNormalizePriority(t *testing.T) {
	tests := []struct {
		name string
		rank int
		want string
	}{
		{"none", 0, PriorityNone},
		{"urgent", 1, PriorityUrgent},
		{"high", 2, PriorityHigh},
		{"medium", 3, PriorityMedium},
		{"low", 4, PriorityLow},
		{"negative unknown", -1, PriorityNone},
		{"above range unknown", 99, PriorityNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizePriority(tt.rank); got != tt.want {
				t.Errorf("NormalizePriority(%d) = %q, want %q", tt.rank, got, tt.want)
			}
		})
	}
}
