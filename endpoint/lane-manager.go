package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type LaneManager struct {
	WebsocketURL   string
	MaxUtilization int
	IdPrefix     string

  OfferBufferFunc func(chanId uint16, offset uint64, buf []byte)
	Lanes  map[string]*DataLane

	lock       sync.Mutex
	dialer *websocket.Dialer
}

func NewLaneManager(websocketUrl, idPrefix string) *LaneManager {
  return &LaneManager{
		WebsocketURL:   websocketUrl,
		MaxUtilization: 4,

		IdPrefix:    idPrefix,
		Lanes: make(map[string]*DataLane),

		dialer: &websocket.Dialer{
			ReadBufferSize:  1024 * 17,
			WriteBufferSize: 1024,
			Subprotocols:    []string{"plex-backhaul"},
		},
	}
}

type DataLane struct {
	LaneId  string
	Conn    *websocket.Conn
	WritesC chan []byte
	InUse   int
}

// Manage a pool of sockets to hold onto and reuse
func (lm *LaneManager) MaintainSockets(desiredCount int) {
	nextLaneId := 1
	openNextSocket := func() error {
		desiredId := lm.IdPrefix + "-" + strconv.Itoa(nextLaneId)
		nextLaneId++
		return lm.OpenLane(desiredId)
	}

	// Set up initial sockets immediately
	for i := 0; i < desiredCount; i++ {
		if err := openNextSocket(); err != nil {
			log.Println("Failed to open socket:", err)
			break
		}
	}
	log.Println("Opened", len(lm.Lanes), "initial sockets")

	// Refill the pool over time
	for _ = range time.Tick(5 * time.Second) {
		lm.lock.Lock()
		currSocks := len(lm.Lanes)
		lm.lock.Unlock()

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

// Selects and reserves at most the desired number of lanes
func (lm *LaneManager) AllocateLanes(desiredCount int) ([]string, []*DataLane) {
  lm.lock.Lock()
  defer lm.lock.Unlock()

	// select least-utilized sockets
	socks := make([]*DataLane, 0)
	mostUtilized := -1
	var mostUtilizedS *DataLane
	for _, thisSock := range lm.Lanes {
		if thisSock.InUse >= lm.MaxUtilization {
			continue // skip outright if maxed out
		}

		if len(socks) < desiredCount {
			// log.Println("Selecting sock", thisSock.LaneId, thisSock.InUse, "because I need more")
			socks = append(socks, thisSock)
			if mostUtilized < thisSock.InUse {
				mostUtilized = thisSock.InUse
				mostUtilizedS = thisSock
			}
		} else if thisSock.InUse < mostUtilized {
			// we want to replace one
			newMostUtilized := -1
			var newMostUtilizedS *DataLane
			for idx, otherSock := range socks {
				if otherSock == mostUtilizedS {
					// log.Println("Replacing sock", otherSock.LaneId, otherSock.InUse, "with", thisSock.LaneId, thisSock.InUse, "because it's less used than", mostUtilized)
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
			// log.Println("New mostUtil is", newMostUtilized, newMostUtilizedS.LaneId)
			mostUtilized = newMostUtilized
			mostUtilizedS = newMostUtilizedS
		} else {
			// log.Println("Skipping sock", thisSock.LaneId, thisSock.InUse, "because it's not less used than", mostUtilized)
		}
	}

	// reserve the selected sockets
	sockKeys := make([]string, len(socks))
	for idx, sock := range socks {
		sockKeys[idx] = sock.LaneId
		sock.InUse++
		// log.Println("Got", sock.LaneId, "@", sock.InUse)
	}

  return sockKeys, socks
}

// Opens a new websocket
func (lm *LaneManager) OpenLane(desiredId string) error {
	log.Println("Creating socket", desiredId, "...")
	wsConn, _, err := lm.dialer.Dial(lm.WebsocketURL, nil)
	if err != nil {
		return err
	}

	// ask for a predictable ID
	if err := wsConn.WriteMessage(websocket.TextMessage, []byte(desiredId)); err != nil {
		return err
	}

	// first message has our confirmed ID
	messageType, chunk, err := wsConn.ReadMessage()
	if err != nil {
		return err
	}

	if messageType != websocket.TextMessage {
		return fmt.Errorf("First websocket frame was type %v, wanted Text", messageType)
	}

  lane := &DataLane{
		LaneId: string(chunk),
		Conn:   wsConn,
		InUse:  0,
	}

	// add ourselves to inventory
	lm.lock.Lock()
	lm.Lanes[lane.LaneId] = lane
	lm.lock.Unlock()

	go lm.ReadForever(lane)
	return nil
}

func (lm *LaneManager) ReadForever(lane *DataLane) {
	for {
		messageType, p, err := lane.Conn.ReadMessage()
		if err != nil {
			log.Println(err)
			// TODO: shut down?
			break
		}
		if messageType != websocket.BinaryMessage {
			log.Println("got unexpected ws message", messageType, p)
		}

		chanId := binary.LittleEndian.Uint16(p[0:])
    offset := binary.LittleEndian.Uint64(p[2:])
    lm.OfferBufferFunc(chanId, offset, p[10:])
	}

	// clean up
	lm.lock.Lock()
	defer lm.lock.Unlock()
	delete(lm.Lanes, lane.LaneId)
}
