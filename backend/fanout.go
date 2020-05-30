package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net/http"
)

// Fanout streams an HTTP response Body
// and pumps chunks of it over a set of outputs
// Minimal framing is added to allow recombination later

type Fanout struct {
	RequestId  string
	ChanId     uint16
	OutC       chan []byte
	CancelFunc context.CancelFunc
}

func (f *Fanout) PumpTo(sockC chan<- []byte, goneC <-chan struct{}) {
	for buf := range f.OutC {
		select {
		case <-goneC:
			log.Println("Cancelling ")
			f.CancelFunc()
			goneC = nil // so we can go back to pumping
		case sockC <- buf:
			// the write happened :)
		}
	}
}

func (f *Fanout) ReadBody(realResp *http.Response) {
	defer realResp.Body.Close()
	defer close(f.OutC)
	reader := bufio.NewReader(realResp.Body)
	offset := 0
	for {
		buf := make([]byte, 16*1024+10)
		n, err := reader.Read(buf[10:])
		// log.Println("n", n)

		if n > 0 {
			binary.LittleEndian.PutUint16(buf[0:], f.ChanId)
			binary.LittleEndian.PutUint64(buf[2:], uint64(offset))
			f.OutC <- buf[:(n + 10)]
			offset += n
		}

		if err == io.EOF || err == context.Canceled {
			log.Println("Request", f.RequestId, "EOF")
			buf = make([]byte, 10)
			binary.LittleEndian.PutUint16(buf[0:], f.ChanId)
			binary.LittleEndian.PutUint64(buf[2:], uint64(offset))
			f.OutC <- buf
			return
		} else if err != nil {
			panic(err)
		}
	}
}
