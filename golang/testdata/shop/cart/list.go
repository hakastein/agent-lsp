package cart

import "example.com/money"

// List prices an item at its list price.
type List struct{}

// Price is the price of it.
func (List) Price(it Item) money.Amount { return it.Price }

// priceOf asks p for the price of it.
func priceOf(p Pricer, it Item) money.Amount { return p.Price(it) }
