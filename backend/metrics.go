package main

import (
	"log"
	"sync/atomic"

	"github.com/danopia/plex-backhaul/common"
)

func (sh *SocketHeadend) runMetrics(intervalSecs int) {
	emitter, err := common.NewMetricsEmitter("plex-backend", "plex.backend.")
	if err != nil {
		panic(err)
	}
	emitter.CollectForever(intervalSecs, func(batch *common.MetricsBatch) {
		sh.lock.Lock()
		defer sh.lock.Unlock()

		batch.AddGaugeInt("live_sockets", len(sh.backhauls))
		batch.AddGaugeInt("live_fanouts", len(sh.fanouts))
		batch.AddGaugeInt("live_requests", len(sh.cancelFuncs))

		for sockId, sock := range sh.backhauls {
			movedBytes := atomic.SwapInt32(&sock.MovedBytes, 0)
			movedPackets := atomic.SwapInt32(&sock.MovedPackets, 0)
			if movedPackets > 0 {
				log.Println("moved", movedBytes, "bytes to", sockId, "in", movedPackets, "packets")
			}

			sockTag := "plex_sock:" + sockId
			batch.AddCount("socket.bytes", float64(movedBytes), sockTag)
			batch.AddCount("socket.packets", float64(movedPackets), sockTag)
		}

		waitingPackets := 0
		for _, fanout := range sh.fanouts {
			waitingPackets += len(fanout.OutC)
		}
		batch.AddGaugeInt("waiting_packets", waitingPackets)

	})
}
