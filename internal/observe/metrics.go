package observe

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Group []prometheus.Collector

func (g Group) Describe(ch chan<- *prometheus.Desc) {
	for _, collector := range g {
		collector.Describe(ch)
	}
}

func (g Group) Collect(ch chan<- prometheus.Metric) {
	for _, collector := range g {
		collector.Collect(ch)
	}
}

func Duration(name, help string) prometheus.Histogram {
	return prometheus.NewHistogram(prometheus.HistogramOpts{Name: "godb_" + name + "_duration_seconds", Help: help, Buckets: []float64{.00001, .0001, .001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}})
}

func Counter(name, help string, value func() float64) prometheus.Collector {
	return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "godb_" + name + "_total", Help: help}, value)
}

func Gauge(name, help string, value func() float64) prometheus.Collector {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "godb_" + name, Help: help}, value)
}

func Since(histogram prometheus.Observer, start time.Time) {
	histogram.Observe(time.Since(start).Seconds())
}
