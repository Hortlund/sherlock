package main

import (
	"log"
	"net/http"
	"os"
	"sherlock/internal/demo"
	"time"
)

func main() {
	addr := os.Getenv("SHERLOCK_DEMO_ADDR")
	if addr == "" {
		addr = ":9000"
	}
	log.Fatal((&http.Server{Addr: addr, Handler: demo.Handler(), ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}
