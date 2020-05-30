package common

import (
	"runtime"
)

var prevMem *runtime.MemStats = &runtime.MemStats{}

func (mb *MetricsBatch) ObserveGolangRuntime() {

	// this one's easy
	mb.AddGaugeInt("live_goroutines", runtime.NumGoroutine())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	mb.AddGaugeUint64("heap.alloc_bytes", mem.HeapAlloc)
	mb.AddCountUint64("heap.alloc_bytes.total", mem.TotalAlloc-prevMem.TotalAlloc)
	mb.AddGaugeUint64("heap.alloc_objects", mem.HeapObjects)
	mb.AddGaugeUint64("heap.idle_bytes", mem.HeapIdle)

	mb.AddGaugeUint64("mem.total_system_bytes", mem.Sys)
	mb.AddCountUint64("mem.mallocs.total", mem.Mallocs-prevMem.Mallocs)
	mb.AddCountUint64("mem.frees.total", mem.Frees-prevMem.Frees)
	mb.AddGaugeUint64("mem.live_objects", mem.Mallocs-mem.Frees) // not cumulative

	mb.AddGaugeUint64("gc.next_cycle_bytes", mem.NextGC)
	mb.AddGauge("gc.cpu_fraction", mem.GCCPUFraction)
	mb.AddCountUint64("gc.paused_nanos.total", mem.PauseTotalNs-prevMem.PauseTotalNs)
	mb.AddGaugeUint64("gc.paused_nanos", mem.PauseNs[(mem.NumGC+255)%256])
	mb.AddCountUint64("gc.cycles.total", uint64(mem.NumGC-prevMem.NumGC))

}
