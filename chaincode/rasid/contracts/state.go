package contracts

import "fmt"

// Status is a receivable lifecycle state.
type Status string

const (
	StatusRegistered     Status = "REGISTERED"
	StatusAccepted       Status = "ACCEPTED"
	StatusListed         Status = "LISTED"
	StatusFundingPending Status = "FUNDING_PENDING"
	StatusFunded         Status = "FUNDED"
	StatusOverdue        Status = "OVERDUE"
	StatusDefaulted      Status = "DEFAULTED"
	StatusRepaid         Status = "REPAID"
	StatusClosed         Status = "CLOSED"
	StatusRejected       Status = "REJECTED"
	StatusCancelled      Status = "CANCELLED"
)

// transitions is the full state machine. Anything not listed is refused.
var transitions = map[Status][]Status{
	StatusRegistered:     {StatusAccepted, StatusRejected, StatusCancelled},
	StatusAccepted:       {StatusListed, StatusCancelled},
	StatusListed:         {StatusFundingPending, StatusCancelled},
	StatusFundingPending: {StatusFunded, StatusListed},
	StatusFunded:         {StatusOverdue, StatusRepaid},
	StatusOverdue:        {StatusDefaulted, StatusRepaid},
	StatusDefaulted:      {StatusRepaid},
	StatusRepaid:         {StatusClosed},
}

// moveTo changes r.Status if the transition is allowed.
func (r *Receivable) moveTo(next Status) error {
	for _, s := range transitions[r.Status] {
		if s == next {
			r.Status = next
			return nil
		}
	}
	return fmt.Errorf("receivable %s: cannot move from %s to %s", r.ID, r.Status, next)
}

func (r *Receivable) requireStatus(allowed ...Status) error {
	for _, s := range allowed {
		if r.Status == s {
			return nil
		}
	}
	return fmt.Errorf("receivable %s is %s, expected one of %v", r.ID, r.Status, allowed)
}
