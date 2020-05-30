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
	"sync"
	"time"

	"github.com/danopia/plex-backhaul/common"
)

// Paths matching this get the websocket treatment
var tunnelPattern = regexp.MustCompile("/transcode/universal/dash/|/chunk-|download=1|/file.mp4")

var precachePattern = regexp.MustCompile("/([0-9]+)/([0-9]+).m4s$")

type ProxyClient struct {
	BaseURL        string
	DirectURL      *url.URL
	MaxConcurrency int

	Channels   map[uint16]*BackhaulChannel
	nextChanId uint16
	lock       sync.Mutex

	LaneManager *LaneManager

	directProxy *httputil.ReverseProxy
	tunnelProxy *httputil.ReverseProxy
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

		Channels: make(map[uint16]*BackhaulChannel),

		LaneManager: NewLaneManager(wsTarget.String(), hostId),
	}
	proxyClient.LaneManager.OfferBufferFunc = proxyClient.OfferBuffer
	go proxyClient.runMetrics(20) // seconds

	proxyClient.directProxy = httputil.NewSingleHostReverseProxy(target)
	proxyClient.tunnelProxy = &httputil.ReverseProxy{
		Director:  proxyClient.directProxy.Director,
		Transport: proxyClient, // use ourself
	}

	return proxyClient, nil
}

func (pc *ProxyClient) OfferBuffer(chanId uint16, offset uint64, buf []byte) {
	pc.lock.Lock()
	if channel, ok := pc.Channels[chanId]; ok {
		pc.lock.Unlock()
		channel.OfferBuffer(offset, buf)
	} else {
		pc.lock.Unlock()
		log.Println("WARN: got buffer for unknown channel", chanId, "with", len(buf), "bytes")
	}
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
		pc.tunnelProxy.ServeHTTP(res, req)
	} else {
		log.Println("Passthru", req.Method, req.URL.Path)
		pc.directProxy.ServeHTTP(res, req)
	}
}

func (pc *ProxyClient) AllocateReqLanes(req *common.InnerReq) (*BackhaulChannel, io.ReadCloser, error) {
	// select least-utilized sockets
	sockKeys, socks := pc.LaneManager.AllocateLanes(pc.MaxConcurrency)
	if len(socks) < 1 {
		return nil, nil, fmt.Errorf("No sockets available, sorry")
	}

	pc.lock.Lock()
	chanReader, chanWriter := io.Pipe()
	channel := NewBackhaulChannel(socks, chanWriter)
	chanId := pc.nextChanId
	pc.Channels[chanId] = channel
	pc.nextChanId++
	pc.lock.Unlock()

	req.BackhaulIds = sockKeys
	req.ChanId = chanId
	return channel, chanReader, nil
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

// Unconditionally processes the request through the multiplexing tunnel
func (pc *ProxyClient) RoundTrip(req *http.Request) (*http.Response, error) {
	t0 := time.Now()

	wireReq := common.InnerReq{
		Method:  req.Method,
		Path:    req.URL.Path,
		Query:   req.URL.RawQuery,
		Headers: req.Header,
	}
	channel, chanReader, err := pc.AllocateReqLanes(&wireReq)
	if err != nil {
		return nil, err
	}

	tAlloced := time.Now()

	// if precachePattern.MatchString(wireReq.Path) {
	// 	precachePath := precachePattern.ReplaceAllString(wireReq.Path, "/0/$1")
	// 	log.Println("TODO: Precaching", precachePath)
	// }

	wireResp, err := pc.SubmitWireRequest(&wireReq)
	if err != nil {
		return nil, err
	}

	tSubmitted := time.Now()

	// Cleanup when the response is all done
	go func() {
		<-channel.DoneC
		pc.lock.Lock()
		defer pc.lock.Unlock()

		// Delete our registration
		delete(pc.Channels, wireReq.ChanId)
		// Unmark the sockets
		for _, sock := range channel.Sockets {
			sock.InUse--
		}
	}()

	go func(ch <-chan struct{}) {
		<-ch
		if !channel.Done {
			jsonValue, err := json.Marshal(wireResp.RequestId)
			if err != nil {
				panic(err)
			}
			log.Println("Attempting to cancel req", string(jsonValue))
			cancelReq, err := http.NewRequest("POST", pc.BaseURL+"/backhaul/cancel", bytes.NewBuffer(jsonValue))
			if err != nil {
				panic(err)
			}
			cancelReq.Header.Set("Content-Type", "application/json")
			resp, err := httpClient.Do(cancelReq)
			if err != nil {
				panic(err)
			}
			io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}
	}(req.Context().Done())

	realResp := &http.Response{
		Status:     wireResp.Status,
		StatusCode: wireResp.StatusCode,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     wireResp.Headers,
		Body:       chanReader,
	}
	realResp.Header.Add("Server-Timing", fmt.Sprintf("fanout, alloc;dur=%v, submit;dur=%v", tAlloced.Sub(t0).Milliseconds(), tSubmitted.Sub(tAlloced).Milliseconds()))

	return realResp, nil
}
