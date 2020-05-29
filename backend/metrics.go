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

		batch.AddGauge("live_sockets", float64(len(sh.backhauls)))
		batch.AddGauge("live_fanouts", float64(len(sh.fanouts)))
		batch.AddGauge("live_requests", float64(len(sh.cancelFuncs)))

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
		batch.AddGauge("waiting_packets", float64(waitingPackets))

	})
}
