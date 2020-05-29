package main

import (
	"bytes"
	"io"
	"io/ioutil"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/danopia/plex-backhaul/common"
)

// Paths matching this get the websocket treatment
var tunnelPattern = regexp.MustCompile("/transcode/universal/dash/|/chunk-|download=1|/file.mp4")

type ProxyClient struct {
	BaseURL        string
	DirectURL      *url.URL
	WebsocketURL   string
	MaxConcurrency int
	MaxUtilization int

	hostId     string
	Backhauls  map[string]*BackhaulSocket
	Channels   map[uint16]*BackhaulChannel
	nextChanId uint16
	lock       sync.Mutex

	socketDialer *websocket.Dialer
	directProxy  *httputil.ReverseProxy
	tunnelProxy  *httputil.ReverseProxy
}

type BackhaulSocket struct {
	SockId  string
	Conn    *websocket.Conn
	WritesC chan []byte
	InUse   int
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
		WebsocketURL:   wsTarget.String(),
		MaxConcurrency: 4,
		MaxUtilization: 4,

		hostId:    hostId,
		Backhauls: make(map[string]*BackhaulSocket),
		Channels:  make(map[uint16]*BackhaulChannel),

		socketDialer: &websocket.Dialer{
			ReadBufferSize:  1024 * 17,
			WriteBufferSize: 1024,
			Subprotocols:    []string{"plex-backhaul"},
		},
	}
	go proxyClient.runMetrics(10) // seconds

	proxyClient.directProxy = httputil.NewSingleHostReverseProxy(target)
	proxyClient.tunnelProxy = &httputil.ReverseProxy{
		Director:  proxyClient.directProxy.Director,
		Transport: proxyClient, // use ourself
	}

	return proxyClient, nil
}

// Manage a pool of sockets to hold onto and reuse
func (pc *ProxyClient) MaintainSockets(desiredCount int) {
	nextSockId := 1
	openNextSocket := func() error {
		desiredId := pc.hostId + "-" + strconv.Itoa(nextSockId)
		nextSockId++

		_, err := pc.ConnectNewBackhaul(desiredId)
		return err
	}

	// Set up initial sockets immediately
	for i := 0; i < desiredCount; i++ {
		// openNextSocket
		// desiredId := pc.hostId + "-" + strconv.Itoa(nextSockId)
		// nextSockId++
		//
		// _, err := pc.ConnectNewBackhaul(desiredId)
		if err := openNextSocket(); err != nil {
			log.Println("Failed to open socket:", err)
			break
		}
	}
	log.Println("Opened", len(pc.Backhauls), "initial sockets")

	// Refill the pool over time
	for _ = range time.Tick(5 * time.Second) {
		pc.lock.Lock()
		currSocks := len(pc.Backhauls)
		pc.lock.Unlock()

		opened := 0
		for i := currSocks; i < desiredCount; i++ {
			if err := openNextSocket(); err != nil {
				log.Println("Failed to open socket:", err)
				break
			} else {
				opened++
			}
		}
		if opened > 0 {
			log.Println("Opened", opened, "more sockets")
		}
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

// Unconditionally processes the request through the multiplexing tunnel
func (pc *ProxyClient) RoundTrip(req *http.Request) (*http.Response, error) {
	t0 := time.Now()
	pc.lock.Lock()

	// select least-utilized sockets
	socks := make([]*BackhaulSocket, 0)
	mostUtilized := -1
	var mostUtilizedS *BackhaulSocket
	for _, thisSock := range pc.Backhauls {
		if thisSock.InUse >= pc.MaxUtilization {
			continue // skip outright if maxed out
		}

		if len(socks) < pc.MaxConcurrency {
			// log.Println("Selecting sock", thisSock.SockId, thisSock.InUse, "because I need more")
			socks = append(socks, thisSock)
			if mostUtilized < thisSock.InUse {
				mostUtilized = thisSock.InUse
				mostUtilizedS = thisSock
			}
		} else if thisSock.InUse < mostUtilized {
			// we want to replace one
			newMostUtilized := -1
			var newMostUtilizedS *BackhaulSocket
			for idx, otherSock := range socks {
				if otherSock == mostUtilizedS {
					// log.Println("Replacing sock", otherSock.SockId, otherSock.InUse, "with", thisSock.SockId, thisSock.InUse, "because it's less used than", mostUtilized)
					socks[idx] = thisSock
					if newMostUtilized < thisSock.InUse {
						newMostUtilized = thisSock.InUse
						newMostUtilizedS = thisSock
					}
				} else if newMostUtilized > otherSock.InUse {
					newMostUtilized = otherSock.InUse
					newMostUtilizedS = otherSock
				}
			}
			// log.Println("New mostUtil is", newMostUtilized, newMostUtilizedS.SockId)
			mostUtilized = newMostUtilized
			mostUtilizedS = newMostUtilizedS
		} else {
			// log.Println("Skipping sock", thisSock.SockId, thisSock.InUse, "because it's not less used than", mostUtilized)
		}
	}

	// reserve the selected sockets
	sockKeys := make([]string, len(socks))
	for idx, sock := range socks {
		sockKeys[idx] = sock.SockId
		sock.InUse++
		// log.Println("Got", sock.SockId, "@", sock.InUse)
	}
	if len(socks) < 1 {
		return nil, fmt.Errorf("No sockets available, sorry")
	}

	channel, chanReader := NewBackhaulChannel(socks)
	chanId := pc.nextChanId
	pc.Channels[chanId] = channel
	pc.nextChanId++

	pc.lock.Unlock()
	tAlloced := time.Now()

	wireReq := common.InnerReq{
		Method:      req.Method,
		Path:        req.URL.Path,
		Query:       req.URL.RawQuery,
		Headers:     req.Header,
		BackhaulIds: sockKeys,
		ChanId:      chanId,
	}

	jsonValue, _ := json.Marshal(wireReq)
	// log.Println(string(jsonValue))

	// resp, err := http.Post(pc.BaseURL+"/backhaul/submit", "application/json", bytes.NewBuffer(jsonValue))
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
	tSubmitted := time.Now()

	// Cleanup when the response is all done
	go func() {
		<-channel.DoneC
		pc.lock.Lock()
		defer pc.lock.Unlock()

		// Delete our registration
		delete(pc.Channels, chanId)
		// Unmark the sockets
		for _, sock := range socks {
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
			// resp, err := http.Post(pc.BaseURL+"/backhaul/cancel", "application/json", bytes.NewBuffer(jsonValue))
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

	// for k, vs := range wireResp.Headers {
	// 	for _, v := range vs {
	// 		realReq.Header.Add(k, v)
	// 	}
	// }

	// log.Println("returning tunneled response")
	return realResp, nil
}

// Opens a new websocket
func (pc *ProxyClient) ConnectNewBackhaul(desiredId string) (string, error) {
	log.Println("Creating socket", desiredId, "...")
	wsConn, _, err := pc.socketDialer.Dial(pc.WebsocketURL, nil)
	if err != nil {
		return "", err
	}

	// ask for a predictable ID
	if err := wsConn.WriteMessage(websocket.TextMessage, []byte(desiredId)); err != nil {
		return "", err
	}

	// first message has our confirmed ID
	messageType, chunk, err := wsConn.ReadMessage()
	if err != nil {
		return "", err
	}

	if messageType != websocket.TextMessage {
		return "", fmt.Errorf("First websocket frame was type %v, wanted Text", messageType)
	}
	backhaulId := string(chunk)

	// add ourselves to inventory
	pc.lock.Lock()
	pc.Backhauls[backhaulId] = &BackhaulSocket{
		SockId: backhaulId,
		Conn:   wsConn,
		InUse:  0,
	}
	pc.lock.Unlock()

	go pc.ReadForever(backhaulId)
	return backhaulId, nil
}

func (pc *ProxyClient) ReadForever(backhaulId string) {
	backhaul := pc.Backhauls[backhaulId]

	// defer close(backhaul.HealthC)
	for {
		messageType, p, err := backhaul.Conn.ReadMessage()
		if err != nil {
			log.Println(err)
			// TODO: shut down?
			break
		}
		if messageType != websocket.BinaryMessage {
			log.Println("got unexpected ws message", messageType, p)
		}

		chanId := binary.LittleEndian.Uint16(p[0:])
		pc.lock.Lock()
		if channel, ok := pc.Channels[chanId]; ok {
			pc.lock.Unlock()
			offset := binary.LittleEndian.Uint64(p[2:])
			channel.OfferBuffer(offset, p[10:])
		} else {
			pc.lock.Unlock()
			log.Println("WARN: got buffer for unknown channel", chanId, "with", len(p[0:]), "bytes")
		}
	}

	// clean up
	pc.lock.Lock()
	defer pc.lock.Unlock()

	delete(pc.Backhauls, backhaulId)
}
