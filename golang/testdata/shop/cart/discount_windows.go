package cart

import "example.com/money"

// Discount prices an item at half its price.
type Discount struct{}

// Price is half the price of it.
func (Discount) Price(it Item) money.Amount { return it.Price / 2 }

// addPriced adds it to c at the price p gives.
func addPriced(c *Cart, p Pricer, it Item) {
	it.Price = p.Price(it)
	c.Add(it)
}
