package main

import (
	"log"

	"github.com/danopia/plex-backhaul/common"
)

func (pc *ProxyClient) runMetrics(intervalSecs int) {
	emitter, err := common.NewMetricsEmitter("plex-endpoint", "plex.endpoint.")
	if err != nil {
		panic(err)
	}
	emitter.CollectForever(intervalSecs, func(batch *common.MetricsBatch) {

		pc.LaneManager.lock.Lock()
		batch.AddGauge("live_sockets", float64(len(pc.LaneManager.Lanes)))

		for sockId, sock := range pc.LaneManager.Lanes {
			sockTag := "plex_sock:" + sockId

			if sock.InUse > 0 {
				log.Println("socket", sockId, "in used by", sock.InUse, "channels")
			}
			batch.AddGauge("socket.inuse", float64(sock.InUse), sockTag)
		}
		pc.LaneManager.lock.Unlock()

		pc.lock.Lock()
		batch.AddGauge("live_channels", float64(len(pc.Channels)))

		waitingPackets := 0
		blockedSockets := 0
		for _, channel := range pc.Channels {
			waitingPackets += len(channel.OutC)
			blockedSockets += len(channel.readySocks)
		}
		pc.lock.Unlock()
		batch.AddGauge("waiting_packets", float64(waitingPackets))
		batch.AddGauge("socket.blocked", float64(blockedSockets))
	})
}
