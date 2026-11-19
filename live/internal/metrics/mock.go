package metrics

import (
	"math/rand"
	"strconv"
	"sync"
	"time"
)

// MockQueryer 在没有真实 Prometheus 时提供可复现的假数据，
// 用于 control plane 自测和前端联调。绝不在生产模式启用。
type MockQueryer struct {
	mu       sync.Mutex
	baseline int
	phase    string
}

func NewMockQueryer() *MockQueryer { return &MockQueryer{baseline: 6, phase: "init"} }

func (m *MockQueryer) Query(promQL string) (*Vector, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case contains(promQL, "replicas_ready"):
		ready := m.baseline
		if m.phase == "degraded" {
			ready = m.baseline - 2
		}
		return vectorOf(float64(ready)), nil
	case contains(promQL, "kube_deployment_status_replicas{"):
		return vectorOf(float64(m.baseline)), nil
	case contains(promQL, "restarts_total"):
		if m.phase == "degraded" {
			return vectorOf(0.35 + rand.Float64()*0.2), nil
		}
		return vectorOf(0), nil
	case contains(promQL, "apiserver_request_duration"):
		return vectorOf(0.03 + rand.Float64()*0.02), nil
	}
	return &Vector{ResultType: "vector"}, nil
}

func (m *MockQueryer) SetPhase(p string) {
	m.mu.Lock()
	m.phase = p
	m.mu.Unlock()
}

func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func vectorOf(v float64) *Vector {
	return &Vector{
		ResultType: "vector",
		Result: []Sample{{
			Metric: map[string]string{"mock": "true"},
			SampleValue: [2]interface{}{float64(time.Now().Unix()), strconv.FormatFloat(v, 'f', 3, 64)},
		}},
	}
}
