package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/danopia/plex-backhaul/common"
)

// Paths matching this get the websocket treatment
var tunnelPattern = regexp.MustCompile("/transcode/universal/dash/|/chunk-|download=1|/file.mp4")

var dashPathPattern = regexp.MustCompile("^(/video/:/transcode/universal/dash/[^/]+/[0-9]+/)([0-9]+)(\\.[^.]+)$")

type ProxyClient struct {
	BaseURL        string
	DirectURL      *url.URL
	MaxConcurrency int

	LaneManager *LaneManager

	directProxy *httputil.ReverseProxy
	directTally uint64
	tunnelProxy *httputil.ReverseProxy
	tunnelTally uint64
}

func NewProxyClient(targetUrl, hostId string) (*ProxyClient, error) {

	target, err := url.Parse(targetUrl)
	if err != nil {
		return nil, err
	}

	wsTarget, err := url.Parse(targetUrl)
	if err != nil {
		return nil, err
	}
	switch wsTarget.Scheme {
	case "http":
		wsTarget.Scheme = "ws"
	case "https":
		wsTarget.Scheme = "wss"
	default:
		return nil, fmt.Errorf("NewProxyTarget can't handle %v scheme", wsTarget.Scheme)
	}
	wsTarget.Path += "/backhaul/socket"

	proxyClient := &ProxyClient{
		BaseURL:        targetUrl,
		DirectURL:      target,
		MaxConcurrency: 4,

		LaneManager: NewLaneManager(wsTarget.String(), hostId),
	}
	go proxyClient.runMetrics(20) // seconds

	proxyClient.directProxy = httputil.NewSingleHostReverseProxy(target)
	proxyClient.tunnelProxy = &httputil.ReverseProxy{
		Director:  proxyClient.directProxy.Director,
		Transport: proxyClient, // use ourself
	}

	return proxyClient, nil
}

var httpClient *http.Client = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 10,
	},
	// Timeout: time.Duration(RequestTimeout) * time.Second,
}

// Makes a decision about how a request should be serviced
func (pc *ProxyClient) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	// Update the headers to allow for SSL redirection
	// TODO: is this necesary?
	req.URL.Host = pc.DirectURL.Host
	req.URL.Scheme = pc.DirectURL.Scheme
	req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
	req.Host = pc.DirectURL.Host

	if tunnelPattern.MatchString(req.URL.Path + "?" + req.URL.RawQuery) {
		log.Println("Tunneling", req.Method, req.URL.Path)
		atomic.AddUint64(&pc.tunnelTally, 1)
		pc.tunnelProxy.ServeHTTP(res, req)
	} else {
		log.Println("Passthru", req.Method, req.URL.Path)
		atomic.AddUint64(&pc.directTally, 1)
		pc.directProxy.ServeHTTP(res, req)
	}
}

func (pc *ProxyClient) SubmitWireRequest(wireReq *common.InnerReq) (*common.InnerResp, error) {
	jsonValue, err := json.Marshal(wireReq)
	if err != nil {
		return nil, err
	}
	// log.Println(string(jsonValue))

	submitReq, err := http.NewRequest("POST", pc.BaseURL+"/backhaul/submit", bytes.NewBuffer(jsonValue))
	if err != nil {
		return nil, err
	}
	submitReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(submitReq)
	if err != nil {
		return nil, err
	}

	var wireResp common.InnerResp
	err = json.NewDecoder(resp.Body).Decode(&wireResp)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	return &wireResp, nil
}

func (pc *ProxyClient) CancelRequest(requestId string) error {
	jsonValue, err := json.Marshal(requestId)
	if err != nil {
		return err
	}
	log.Println("Attempting to cancel req", string(jsonValue))
	cancelReq, err := http.NewRequest("POST", pc.BaseURL+"/backhaul/cancel", bytes.NewBuffer(jsonValue))
	if err != nil {
		return err
	}
	cancelReq.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(cancelReq)
	if err != nil {
		return err
	}
	io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// Unconditionally processes the request through the multiplexing tunnel
func (pc *ProxyClient) RoundTrip(req *http.Request) (*http.Response, error) {
	t0 := time.Now()

	if dashMatch := dashPathPattern.FindStringSubmatch(req.URL.Path); dashMatch != nil {
		log.Println("TODO: DASH request for segment", dashMatch[2])
	}

	bundle, err := pc.LaneManager.AllocateBundle(pc.MaxConcurrency)
	if err != nil {
		return nil, err
	}

	wireReq := common.InnerReq{
		Method:  req.Method,
		Path:    req.URL.Path,
		Query:   req.URL.RawQuery,
		Headers: req.Header,

		BackhaulIds: bundle.LaneIDs(),
		ChanId:      bundle.BundleId,
	}

	wireResp, err := pc.SubmitWireRequest(&wireReq)
	if err != nil {
		return nil, err
	}
	bundle.RequestId = wireResp.RequestId

	tSubmitted := time.Now()

	// Start reassembling the buffers into a Reader
	chanReader, chanWriter := io.Pipe()
	go bundle.PumpOut(chanWriter)

	go func(ch <-chan struct{}) {
		<-ch
		if !bundle.Done {
			pc.CancelRequest(bundle.RequestId)
		}
	}(req.Context().Done())

	realResp := MakeHttpResponse(wireResp, chanReader)
	realResp.Header.Add("Server-Timing", fmt.Sprintf(
		"fanout, submit;dur=%v",
		tSubmitted.Sub(t0).Milliseconds()))

	return realResp, nil
}

func MakeHttpResponse(ir *common.InnerResp, body io.ReadCloser) *http.Response {
	return &http.Response{
		Status:     ir.Status,
		StatusCode: ir.StatusCode,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     ir.Headers,
		Body:       body,
	}
}
