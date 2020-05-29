package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}

	proxyClient, err := NewProxyClient("http://redacted:2019", hostname)
	if err != nil {
		log.Fatal(err)
	}
	proxyClient.MaxConcurrency = 4
	go proxyClient.LaneManager.MaintainSockets(8)

	// start server
	log.Println("lets go")
	// if err := http.ListenAndServe(":5000", proxyClient); err != nil {
	if err := http.ListenAndServeTLS(":5000", "server.crt", "server.key", proxyClient); err != nil {
		log.Fatal(err)
	}
}
