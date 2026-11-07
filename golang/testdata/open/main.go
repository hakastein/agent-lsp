// Command open opens the Go index of the root given as its argument and closes it: a process of a program that
// imports the library.
package main

import (
	"log"
	"os"

	"github.com/hakastein/agent-lsp/golang"
)

func main() {
	ix, err := golang.Open(os.Args[1], log.Printf)
	if err != nil {
		log.Fatal(err)
	}
	if err := ix.Close(); err != nil {
		log.Fatal(err)
	}
}
