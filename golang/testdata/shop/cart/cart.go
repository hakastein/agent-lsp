// Package cart keeps the items a customer is about to buy.
package cart

import (
	"fmt"

	"example.com/money"
)

// Cart is the items of one customer.
type Cart struct {
	Items []Item // in the order they were added
	total money.Amount
}

// Item is one line of a cart.
type Item struct {
	Name  string
	Price money.Amount
}

// Add puts an item into the cart
// and adds its price to the total.
func (c *Cart) Add(it Item) {
	c.Items = append(c.Items, it)
	c.total = c.total.Plus(it.Price)
}

// Total is the sum of the prices.
func (c *Cart) Total() money.Amount { return c.total }

func (c *Cart) String() string {
	return fmt.Sprintf("%d items", len(c.Items))
}

// Limits of a cart.
const (
	// MaxItems is the most items a cart holds.
	MaxItems = 100
	MinItems = 0
)

// Pricer prices an item.
type Pricer interface {
	// Price is the price of it.
	Price(it Item) money.Amount
}
