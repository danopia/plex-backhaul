package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gorilla/websocket"

	"github.com/danopia/plex-backhaul/common"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024 * 17,
	Subprotocols:    []string{"plex-backhaul"},
}

func main() {
	seedRandom()

	upstreamUrl := "https://redacted.plex.direct:32400"

	// parse the url
	proxyTarget, err := url.Parse(upstreamUrl)
	if err != nil {
		log.Fatal(err)
	}

	headend := NewSocketHeadend(upstreamUrl)
	go headend.runMetrics(10) // seconds

	// Mount a normal proxy to Plex at the root
	directProxy := httputil.NewSingleHostReverseProxy(proxyTarget)
	http.HandleFunc("/", func(res http.ResponseWriter, req *http.Request) {
		// log.Println("Passthru:", req.Method, req.URL.Path)
		directProxy.ServeHTTP(res, req)
	})

	http.HandleFunc("/backhaul/submit", func(res http.ResponseWriter, req *http.Request) {
		var innerReq common.InnerReq
		err := json.NewDecoder(req.Body).Decode(&innerReq)
		if err != nil {
			http.Error(res, err.Error(), http.StatusBadRequest)
			return
		}

		if innerResp, err := headend.SubmitRequest(&innerReq); err != nil {
			http.Error(res, err.Error(), http.StatusBadRequest)
		} else {
			res.Header().Set("Content-Type", "application/json")
			json.NewEncoder(res).Encode(innerResp)
		}
	})

	http.HandleFunc("/backhaul/cancel", func(res http.ResponseWriter, req *http.Request) {
		var requestId string
		err := json.NewDecoder(req.Body).Decode(&requestId)
		if err != nil {
			http.Error(res, err.Error(), http.StatusBadRequest)
			return
		}

		if ok := headend.CancelRequest(requestId); ok {
			fmt.Fprintf(res, "Cancelled!")
		} else {
			http.NotFound(res, req)
			fmt.Fprintf(res, "Unknown request ID!")
		}
	})

	// Special endpoint for the endpoint to connect to for its byte streams
	http.HandleFunc("/backhaul/socket", func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Sec-Websocket-Protocol") != "plex-backhaul" {
			http.Error(res, "Incorrect WS Protocol", http.StatusForbidden)
			return
		}

		conn, err := upgrader.Upgrade(res, req, nil)
		if err != nil {
			log.Println("Backhaul upgrade err:", err)
			http.Error(res, err.Error(), http.StatusBadRequest)
			return
		}

		if err := headend.EstablishBackhaul(conn); err != nil {
			log.Println("Backhaul setup err:", err)
			http.Error(res, err.Error(), http.StatusBadRequest)
			return
		}
	})

	// start server
	log.Println("lets go")
	if err := http.ListenAndServe(":2019", nil); err != nil {
		log.Fatal(err)
	}
}
