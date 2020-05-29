package common

import (
	"log"
	"os"
	"os/user"
	"time"

	"github.com/zorkian/go-datadog-api"
)

type MetricsEmitter struct {
	metricPrefix string
	hostname     *string
	commonTags   []string
	client       *datadog.Client
}

func NewMetricsEmitter(appName string, metricPrefix string) (*MetricsEmitter, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	user, err := user.Current()
	if err != nil {
		return nil, err
	}

	return &MetricsEmitter{
		metricPrefix: metricPrefix,
		hostname:     datadog.String(hostname),
		commonTags: []string{
			"app:" + appName,
			// app_version: `${packageName}/${packageInfo.version}`,
			"host_user:" + user.Username,
			// host_ipv6: v6Prefix,
			// host_os: `${os.type()} ${os.release()}`,
		},
		client: datadog.NewClient("redacted", ""),
	}, nil
}

func (me *MetricsEmitter) CollectForever(intervalSecs int, collectorFunc func(*MetricsBatch)) {
	for now := range time.Tick(time.Duration(intervalSecs) * time.Second) {

		batchDate := float64(now.Unix() - int64(intervalSecs/2))
		batch := &MetricsBatch{
			emitter:   me,
			batchDate: datadog.Float64(batchDate),
		}

		collectorFunc(batch)

		if len(batch.metrics) > 0 {
			err := me.client.PostMetrics(batch.metrics)
			if err != nil {
				log.Println("metrics error", err)
			} else {
				log.Println("Submitted", len(batch.metrics), "metrics")
			}
		}
	}
}
