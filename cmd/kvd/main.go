// Command kvd runs a single-node in-memory key-value store with an HTTP/JSON API.
package main

import (
	"flag"
	"log"
	"net/http"

	"kvd/api"
	"kvd/store"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()
	h := api.New(store.NewMem())
	log.Printf("kvd listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, h))
}
