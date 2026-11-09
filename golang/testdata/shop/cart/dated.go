package cart

import "time"

// Dated falls on a day of the week.
type Dated interface {
	Weekday() time.Weekday
}

var _ Dated = time.Time{}
