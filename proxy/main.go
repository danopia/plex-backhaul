package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
  "regexp"
)

// Paths matching this get the websocket treatment
var tunnelPattern = regexp.MustCompile("/transcode/universal/dash/|/chunk-")

func main() {
	targetUrlPtr := flag.String("target-url", "", "Origin URL to the Plex server (often starting with https:// and using port :32400)")
	enableSslPtr := flag.Bool("enable-ssl", false, "Serve self-signed TLS instead of bare HTTP")
	flag.Parse()

  // parse the url
	proxyTarget, err := url.Parse(*targetUrlPtr)
  if err != nil {
    log.Fatal(err)
  }

  directProxy := httputil.NewSingleHostReverseProxy(proxyTarget)

	http.HandleFunc("/", func (res http.ResponseWriter, req *http.Request) {
    // Update the headers to allow for SSL redirection
    // TODO: is this necesary?
    req.URL.Host = proxyTarget.Host
    req.URL.Scheme = proxyTarget.Scheme
    req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
    req.Host = proxyTarget.Host

		// log.Printf("%+v", req.URL)
    log.Println(req.Method, req.URL.Path)
    if tunnelPattern.MatchString(req.URL.Path) {
      log.Println("^^ using tunnel")

    	tunnelProxy := &httputil.ReverseProxy{
        Director: directProxy.Director,
        Transport: &ParallelTransport{},
      }
      tunnelProxy.ServeHTTP(res, req)
    } else {
      directProxy.ServeHTTP(res, req)
    }
  })

  // start server
	setupClient()
  log.Println("lets go")
	if *enableSslPtr {
		if err := http.ListenAndServeTLS(":5000", "server.crt", "server.key", nil); err != nil {
			log.Fatal(err)
		}
	} else {
		if err := http.ListenAndServe(":5000", nil); err != nil {
			log.Fatal(err)
		}
	}
}
