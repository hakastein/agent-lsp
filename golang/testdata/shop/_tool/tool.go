// Package main is a tool the go command leaves out of the packages of the module: it skips directories that start
// with _. A question about it loads its package then.
package main

import "example.com/shop/cart"

var basket cart.Cart
