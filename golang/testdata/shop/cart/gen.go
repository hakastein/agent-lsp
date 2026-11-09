//go:build ignore

// Gen is a program of its own, run by go run gen.go: its MinItems is not that of the package cart.
package main

const MinItems = 1

func main() { println(MinItems) }
