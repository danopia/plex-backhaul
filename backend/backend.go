package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"

	"github.com/danopia/plex-backhaul/common"
)

type SocketHeadend struct {
	upstreamUrl string
	backhauls   map[string]*BackhaulSocket
	fanouts     map[string]*Fanout
	cancelFuncs map[string]context.CancelFunc
	lock        sync.Mutex
}

func NewSocketHeadend(upstreamUrl string) *SocketHeadend {
	return &SocketHeadend{
		upstreamUrl: upstreamUrl,
		backhauls:   make(map[string]*BackhaulSocket),
		fanouts:     make(map[string]*Fanout),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
}

type BackhaulSocket struct {
	Conn    *websocket.Conn
	GoneC   chan struct{}
	WritesC chan []byte
	// InUse   bool

	MovedBytes   int32
	MovedPackets int32
}

func (sh *SocketHeadend) EstablishBackhaul(wsConn *websocket.Conn) error {

	// first message has client's desired ID
	messageType, chunk, err := wsConn.ReadMessage()
	if err != nil {
		return err
	}
	if messageType != websocket.TextMessage {
		return fmt.Errorf("First websocket frame was type %v, wanted Text", messageType)
	}
	backhaulId := string(chunk)

	sh.lock.Lock()
	// backhaulId := randSeq(6)
	if _, ok := sh.backhauls[backhaulId]; ok {
		return errors.New("oops, backhaul id overlap")
	}
	sh.backhauls[backhaulId] = &BackhaulSocket{
		Conn:    wsConn,
		GoneC:   make(chan struct{}),
		WritesC: make(chan []byte),
		// InUse:   false,
	}
	sh.lock.Unlock()

	if err := wsConn.WriteMessage(websocket.TextMessage, []byte(backhaulId)); err != nil {
		return err
	}

	go sh.ReadForever(backhaulId)
	go sh.PumpWrites(backhaulId)

	log.Printf("backhaul sock %v connected", backhaulId)
	return nil
}

func (sh *SocketHeadend) ReadForever(backhaulId string) {
	backhaul := sh.backhauls[backhaulId]

	defer close(backhaul.GoneC)
	for {
		messageType, p, err := backhaul.Conn.ReadMessage()
		if err != nil {
			log.Println("ReadForever:", err)
			// TODO: shut down?
			break
		}
		log.Println("got", messageType, p)
		panic("TODO: why tf did i receive a message")
	}

	// clean up
	sh.lock.Lock()
	defer sh.lock.Unlock()
	delete(sh.backhauls, backhaulId)
}

func (sh *SocketHeadend) PumpWrites(backhaulId string) {
	backhaul := sh.backhauls[backhaulId]

	// defer close(backhaul.WritesC)
	for buf := range backhaul.WritesC {
		err := backhaul.Conn.WriteMessage(websocket.BinaryMessage, buf)
		if err != nil {
			log.Println("PumpWrites:", err)
			// TODO: need to know when we have zero requests so we can close WritesC
		} else {
			// log.Println("wrote", len(buf), "bytes to", backhaulId)
			atomic.AddInt32(&backhaul.MovedBytes, int32(len(buf)))
			atomic.AddInt32(&backhaul.MovedPackets, 1)
		}
	}
}

func (sh *SocketHeadend) CancelRequest(requestId string) (ok bool) {
	log.Println("Canceling request", requestId)
	sh.lock.Lock()
	defer sh.lock.Unlock()

	cancelFunc, ok := sh.cancelFuncs[requestId]
	if ok {
		cancelFunc()
	}
	return
}

func (sh *SocketHeadend) SubmitRequest(innerReq *common.InnerReq) (*common.InnerResp, error) {
	if len(innerReq.BackhaulIds) < 1 {
		return nil, errors.New("BUG: You need to specify at least one backhaul ID")
	}

	// Register request ID for cancellation
	sh.lock.Lock()
	requestId := randSeq(12)
	if _, ok := sh.cancelFuncs[requestId]; ok {
		return nil, errors.New("oops, channel ctx id overlap")
	}
	chanCtx, cancelFunc := context.WithCancel(context.TODO())
	sh.cancelFuncs[requestId] = cancelFunc
	sh.lock.Unlock()

	// Build fresh request to send upstream to Plex
	realReq, err := http.NewRequestWithContext(chanCtx, innerReq.Method, sh.upstreamUrl, nil)
	if err != nil {
		return nil, err
	}
	// Copy path and headers
	realReq.URL.Path = innerReq.Path
	realReq.URL.RawQuery = innerReq.Query
	for k, vs := range innerReq.Headers {
		for _, v := range vs {
			realReq.Header.Add(k, v)
		}
	}

	// Issue upstream request
	log.Println("Request", requestId, ":", innerReq.Method, innerReq.Path)
	client := http.Client{
		// Timeout: time.Duration(50 * time.Second),
	}
	realResp, err := client.Do(realReq)
	if err != nil {
		return nil, err
	}

	// Register a Fanout
	// TODO: make a FannedRequest type
	sh.lock.Lock()
	fanout := &Fanout{
		RequestId:  requestId,
		ChanId:     uint16(innerReq.ChanId),
		OutC:       make(chan []byte, 4),
		CancelFunc: cancelFunc,
	}
	sh.fanouts[requestId] = fanout
	sh.lock.Unlock()

	// wire Fanout input and output[s]
	for _, sockId := range innerReq.BackhaulIds {
		backhaul := sh.backhauls[sockId]
		go fanout.PumpTo(backhaul.WritesC, backhaul.GoneC)
	}

	go func() {
		fanout.ReadBody(realResp)

		// Unregister cancellation function
		sh.lock.Lock()
		delete(sh.cancelFuncs, requestId)
		delete(sh.fanouts, requestId)
		sh.lock.Unlock()
	}()

	return &common.InnerResp{
		RequestId:  requestId,
		Status:     realResp.Status,
		StatusCode: realResp.StatusCode,
		Headers:    realResp.Header,
	}, nil
}
