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

		pc.LaneManager.lock.Lock()
		batch.AddGaugeInt("live_sockets", len(pc.LaneManager.Lanes))

		for sockId, sock := range pc.LaneManager.Lanes {
			sockTag := "plex_sock:" + sockId

			if sock.InUse > 0 {
				log.Println("socket", sockId, "in used by", sock.InUse, "channels")
			}
			batch.AddGaugeInt("socket.inuse", sock.InUse, sockTag)
		}
		pc.LaneManager.lock.Unlock()

		pc.lock.Lock()
		batch.AddGaugeInt("live_channels", len(pc.Channels))

		batch.AddCountUint64("http_requests", atomic.SwapUint64(&pc.directTally, 0), "transport:direct")
		batch.AddCountUint64("http_requests", atomic.SwapUint64(&pc.tunnelTally, 0), "transport:tunnel")

		waitingPackets := 0
		blockedSockets := 0
		for _, channel := range pc.Channels {
			waitingPackets += len(channel.OutC)
			blockedSockets += len(channel.readySocks)
		}
		pc.lock.Unlock()
		batch.AddGaugeInt("waiting_packets", waitingPackets)
		batch.AddGaugeInt("socket.blocked", blockedSockets)
	})
}
