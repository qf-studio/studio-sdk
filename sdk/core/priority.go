package core

// Normalized priority vocabulary for IssueEvent.Priority. Connectors map their
// tracker-native priority onto these values (via NormalizePriority or directly)
// so the host sees one consistent set across every provider. These are the only
// non-empty values IssueEvent.Priority should ever hold.
const (
	PriorityNone   = "none"
	PriorityUrgent = "urgent"
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"
)

// NormalizePriority maps a tracker-native priority rank to the normalized
// string vocabulary above. The rank convention is shared by every connector's
// local Priority enum:
//
//	0 = None, 1 = Urgent, 2 = High, 3 = Medium, 4 = Low
//
// Any unknown rank (including 0) normalizes to PriorityNone. Centralizing the
// mapping here keeps connectors from each re-implementing the same int→string
// conversion.
func NormalizePriority(rank int) string {
	switch rank {
	case 1:
		return PriorityUrgent
	case 2:
		return PriorityHigh
	case 3:
		return PriorityMedium
	case 4:
		return PriorityLow
	default:
		return PriorityNone
	}
}
