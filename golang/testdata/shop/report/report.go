//go:build ignore

// Report prints the limits of a cart, run by go run report.go.
package main

import "example.com/shop/cart"

func main() {
	MinItems := 1
	println(cart.MaxItems, MinItems, (&cart.Cart{}).Total())
}
