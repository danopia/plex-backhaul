package main

import (
	"io"
	"log"
	"sync"
)

type BackhaulChannel struct {
	FeedC      chan []byte
	OutC       chan []byte
	DoneC      <-chan struct{}
	Sockets    []*BackhaulSocket
	Offset     uint64
	Done       bool
	lock       sync.Mutex
	readySocks map[uint64]*sync.Mutex
}
type chanReader struct {
	reader  *io.PipeReader
	channel *BackhaulChannel
}

func NewBackhaulChannel(socks []*BackhaulSocket) (*BackhaulChannel, io.ReadCloser) {
	r, w := io.Pipe()
	doneC := make(chan struct{})
	channel := &BackhaulChannel{
		FeedC:      make(chan []byte),
		OutC:       make(chan []byte, 25),
		DoneC:      doneC,
		Sockets:    socks,
		readySocks: make(map[uint64]*sync.Mutex),
	}
	go channel.PumpOut(w, doneC)
	go channel.SequenceBody()
	// return channel, &chanReader{r, channel}
	return channel, r
}

func (bc *BackhaulChannel) PumpOut(w *io.PipeWriter, doneC chan<- struct{}) {
	defer w.Close()
	defer close(doneC)
	for buf := range bc.OutC {
		// log.Println("Writing", len(buf), "bytes")
		w.Write(buf)
	}

	log.Println("Output pump reached end")
	bc.lock.Lock()
	bc.Done = true
	// for _, sock := range bc.Sockets {
	// 	sock.InUse -= 1 // TODO: racy
	// }
	bc.lock.Unlock()
}

func (bc *BackhaulChannel) SequenceBody() {
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
func (bc *BackhaulChannel) OfferBuffer(offset uint64, buf []byte) {
	// log.Println("chan got", offset, len(buf))

	// check if we're ready now
	bc.lock.Lock()
	if bc.Offset == offset {
		// log.Println("Channel is ready now")
		bc.lock.Unlock()
	} else if bc.Offset > offset {
		panic("Received buffer out of order")
	} else {
		lock := &sync.Mutex{}
		lock.Lock() // will be unlocked when we're ready
		bc.readySocks[offset] = lock

		// log.Println("Waiting for our turn...", offset)
		bc.lock.Unlock()
		lock.Lock() // actual wait for ready
		// lock.Unlock()
		// bc.lock.Lock()
		// log.Println("It's our turn :)", offset)
	}

	if len(buf) > 0 {
		// bc.Offset += uint64(len(buf))
		// bc.lock.Unlock()
		bc.FeedC <- buf
	} else {
		close(bc.FeedC)
	}
}

func (cr *chanReader) Read(p []byte) (n int, err error) {
	// log.Println("read req", len(p))
	return cr.reader.Read(p)
}
func (cr *chanReader) Close() error {
	log.Println("TODO: submit close() upstream")
	return cr.reader.Close()
}
