// Package money counts amounts in cents.
package money

// Amount is a sum in cents.
type Amount int64

// Plus is the sum of a and b.
func (a Amount) Plus(b Amount) Amount { return a + b }
