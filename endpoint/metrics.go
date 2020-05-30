package main

import (
	"log"
	"sync/atomic"

	"github.com/danopia/plex-backhaul/common"
)

func (pc *ProxyClient) runMetrics(intervalSecs int) {
	emitter, err := common.NewMetricsEmitter("plex-endpoint", "plex.endpoint.")
	if err != nil {
		panic(err)
	}
	emitter.CollectForever(intervalSecs, func(batch *common.MetricsBatch) {
		lm := pc.LaneManager

		lm.lock.Lock()
		lm.lock.Unlock()
		batch.AddGaugeInt("live_sockets", len(lm.Lanes))

		for sockId, sock := range lm.Lanes {
			sockTag := "plex_sock:" + sockId

			if sock.InUse > 0 {
				log.Println("socket", sockId, "in used by", sock.InUse, "channels")
			}
			batch.AddGaugeInt("socket.inuse", sock.InUse, sockTag)
		}

		batch.AddGaugeInt("live_channels", len(lm.Bundles))

		waitingPackets := 0
		blockedSockets := 0
		for _, channel := range lm.Bundles {
			waitingPackets += len(channel.OutC)
			blockedSockets += len(channel.readySocks)
		}
		batch.AddGaugeInt("waiting_packets", waitingPackets)
		batch.AddGaugeInt("socket.blocked", blockedSockets)

		batch.AddCountUint64("http_requests", atomic.SwapUint64(&pc.directTally, 0), "transport:direct")
		batch.AddCountUint64("http_requests", atomic.SwapUint64(&pc.tunnelTally, 0), "transport:tunnel")

	})
}
