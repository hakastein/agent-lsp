package main

import (
	"fmt"

	"example.com/money"
	"example.com/shop/cart"
)

func main() {
	basket := &cart.Cart{}
	basket.Add(cart.Item{Name: "tea", Price: money.Amount(300)})
	fmt.Println(basket.Items, basket.Total())
}
