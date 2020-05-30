package common

import (
	"github.com/zorkian/go-datadog-api"
)

type MetricsBatch struct {
	metricPrefix string
	emitter      *MetricsEmitter
	batchDate    *float64
	metrics      []datadog.Metric
}

func (mb *MetricsBatch) AddGaugeInt(name string, value int, tags ...string) {
	mb.AddGauge(name, float64(value), tags...)
}
func (mb *MetricsBatch) AddGaugeUint64(name string, value uint64, tags ...string) {
	mb.AddGauge(name, float64(value), tags...)
}
func (mb *MetricsBatch) AddGauge(name string, value float64, tags ...string) {
	mb.metrics = append(mb.metrics, datadog.Metric{
		Metric: datadog.String(mb.metricPrefix + name),
		Points: []datadog.DataPoint{{mb.batchDate, datadog.Float64(value)}},
		Type:   datadog.String("gauge"),
		Host:   mb.emitter.hostname,
		Tags:   append(tags, mb.emitter.commonTags...),
		// Unit:
		// Interval:
	})
}

// func (mb *MetricsBatch) AddRate(name string, value float64, tags ...string) {
// 	mb.metrics = append(mb.metrics, datadog.Metric{
// 		Metric: datadog.String(mb.metricPrefix + name),
// 		Points: []datadog.DataPoint{{mb.batchDate, datadog.Float64(value)}},
// 		Type:   datadog.String("rate"),
// 		Host:   mb.emitter.hostname,
// 		Tags:   append(tags, mb.emitter.commonTags...),
// 	})
// }

func (mb *MetricsBatch) AddCountInt(name string, value int, tags ...string) {
	mb.AddCount(name, float64(value), tags...)
}
func (mb *MetricsBatch) AddCountUint64(name string, value uint64, tags ...string) {
	mb.AddCount(name, float64(value), tags...)
}
func (mb *MetricsBatch) AddCount(name string, value float64, tags ...string) {
	mb.metrics = append(mb.metrics, datadog.Metric{
		Metric: datadog.String(mb.metricPrefix + name),
		Points: []datadog.DataPoint{{mb.batchDate, datadog.Float64(value)}},
		Type:   datadog.String("count"),
		Host:   mb.emitter.hostname,
		Tags:   append(tags, mb.emitter.commonTags...),
	})
}
