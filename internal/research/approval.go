package research

// ApprovalTransition evaluates a decision from the authenticated owner. The
// calling transaction still has to bind its action, expiry and resource scope.
func ApprovalTransition(previous, decidedBy, owner, next string) (allowed, replay bool) {
	if owner == "" {
		return false, false
	}
	switch next {
	case "GRANTED", "DENIED":
		return previous == "PENDING", previous == next && decidedBy == owner
	case "CANCELLED":
		return previous == "PENDING" || previous == "GRANTED", previous == next
	default:
		return false, false
	}
}
