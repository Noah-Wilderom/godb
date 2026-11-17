package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkMetricsScrape(b *testing.B) {
	c := setupCluster(b, 1)
	c.start(b, 1)
	handler := c.members[1].MetricsHandler()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	b.ReportAllocs()

	for b.Loop() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			b.Fatal(response.Code)
		}
	}
}
