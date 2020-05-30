package main

import (
	"io"
	"log"
	"sync"
)

type LaneBundle struct {
	BundleId  uint16
	RequestId string
	Lanes     []*DataLane

	FeedC  chan []byte
	OutC   chan []byte
	DoneC  <-chan struct{}
	Offset uint64
	Done   bool

	doneC      chan<- struct{}
	lock       sync.Mutex
	readySocks map[uint64]*sync.Mutex
}

func NewLaneBundle(bundleId uint16, lanes []*DataLane) *LaneBundle {
	doneC := make(chan struct{})
	channel := &LaneBundle{
		BundleId: bundleId,
		Lanes:    lanes,

		FeedC:  make(chan []byte),
		OutC:   make(chan []byte, 25),
		DoneC:  doneC,
		Offset: 0,

		doneC:      doneC,
		readySocks: make(map[uint64]*sync.Mutex),
	}
	// go channel.PumpOut(w, doneC)
	go channel.SequenceBody()
	return channel
}

func (bc *LaneBundle) LaneIDs() []string {
	keys := make([]string, len(bc.Lanes))
	for idx, sock := range bc.Lanes {
		keys[idx] = sock.LaneId
	}
	return keys
}

func (bc *LaneBundle) PumpOut(w io.WriteCloser) {
	defer w.Close()
	defer close(bc.doneC)
	for buf := range bc.OutC {
		// log.Println("Writing", len(buf), "bytes")
		w.Write(buf)
	}

	log.Println("Request", bc.RequestId, "output reached end")
	bc.lock.Lock()
	bc.Done = true
	bc.lock.Unlock()
}

func (bc *LaneBundle) SequenceBody() {
	defer close(bc.OutC)
	for buf := range bc.FeedC {
		bc.OutC <- buf

		// bookkeeping
		bc.lock.Lock()
		bc.Offset += uint64(len(buf))
		if readyLock, ok := bc.readySocks[bc.Offset]; ok {
			// log.Println("unlocking waiting socket for", bc.Offset)
			delete(bc.readySocks, bc.Offset)
			readyLock.Unlock()
		}
		bc.lock.Unlock()
	}
}

func (bc *LaneBundle) OfferBuffer(offset uint64, buf []byte) {
	// log.Println("chan", bc.BundleId, "got", offset, len(buf))

	// check if we're ready now
	bc.lock.Lock()
	if bc.Offset == offset {
		bc.lock.Unlock()
	} else if bc.Offset > offset {
		log.Println(bc.Offset, offset)
		panic("Received buffer out of order")
	} else {
		lock := &sync.Mutex{}
		lock.Lock() // will be unlocked when we're ready
		// log.Println("Waiting for", offset, bc.Offset)
		bc.readySocks[offset] = lock
		bc.lock.Unlock()

		lock.Lock() // actual wait for ready
	}

	if len(buf) > 0 {
		// TODO?: inline FeedC's reader here
		bc.FeedC <- buf
	} else {
		close(bc.FeedC)
	}
}
