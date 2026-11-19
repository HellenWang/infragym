// Package server 暴露 HTTP API 与 SSE 事件流，把集群操作串成一次演练。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/hellenwang/infragym/live/internal/analyze"
	"github.com/hellenwang/infragym/live/internal/chaos"
	"github.com/hellenwang/infragym/live/internal/kube"
	"github.com/hellenwang/infragym/live/internal/metrics"
)

// Server 持有依赖并注册路由。
type Server struct {
	kube      *kube.Client
	injector  *chaos.Injector
	collector *metrics.Collector
	prom      metrics.Queryer
	mockMode  bool

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	id        string
	scenario  string
	events    []analyze.Event
	subs      map[chan analyze.Event]struct{}
	result    *analyze.Result
	startedAt time.Time
	done      bool
	injectID  string // chaos 资源 ID，用于兜底清理
}

// New 构造 Server。k 为 nil 时自动进入 mock 模式。
func New(k *kube.Client, prom metrics.Queryer, namespace, appLabel string) *Server {
	s := &Server{
		kube:      k,
		collector: metrics.NewCollector(prom, namespace, appLabel),
		prom:      prom,
		mockMode:  k == nil,
		sessions:  make(map[string]*session),
	}
	if k != nil {
		s.injector = chaos.NewInjector(k)
	}
	return s
}

// Routes 返回配置好的 mux。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/experiments", s.handleCreate)
	mux.HandleFunc("/api/experiments/", s.handleExperiment)
	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.mockMode {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true, "clusterVersion": "mock-v1", "namespace": "mock", "mock": true,
		})
		return
	}
	ver, err := s.kube.Version()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"ok": false, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true, "clusterVersion": ver,
		"namespace": s.kube.Config().Namespace,
	})
}

// CreateRequest 是启动实验的请求体。
type CreateRequest struct {
	Scenario  string `json:"scenario"`
	Namespace string `json:"namespace"`
	AppLabel  string `json:"appLabel"`
	Duration  int    `json:"duration"` // 秒
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "需要 POST"})
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if req.Scenario == "" {
		req.Scenario = "node-loss"
	}
	if req.Duration <= 0 {
		req.Duration = 30
	}

	id := fmt.Sprintf("exp-%d", time.Now().UnixNano())
	sess := &session{
		id: id, scenario: req.Scenario,
		subs: make(map[chan analyze.Event]struct{}),
		startedAt: time.Now(),
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	go s.runExperiment(sess, req)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"id": id, "stream": "/api/experiments/" + id + "/stream",
	})
}

// handleExperiment 路由 /api/experiments/{id}/stream 和 /api/experiments/{id}。
func (s *Server) handleExperiment(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/experiments/"):]
	if len(rest) >= 8 && rest[len(rest)-7:] == "/stream" {
		s.handleStream(w, r, rest[:len(rest)-7])
		return
	}
	s.handleResult(w, r, rest)
}

func (s *Server) getSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request, id string) {
	sess := s.getSession(id)
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "实验不存在"})
		return
	}
	s.mu.Lock()
	result := sess.result
	events := append([]analyze.Event(nil), sess.events...)
	done := sess.done
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": id, "scenario": sess.scenario,
		"done": done, "result": result, "events": events,
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, id string) {
	sess := s.getSession(id)
	if sess == nil {
		http.Error(w, "实验不存在", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan analyze.Event, 64)
	s.mu.Lock()
	sess.subs[ch] = struct{}{}
	already := append([]analyze.Event(nil), sess.events...)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(sess.subs, ch)
		s.mu.Unlock()
	}()

	send := func(e analyze.Event) {
		data, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: tick\ndata: %s\n\n", data)
		flusher.Flush()
	}
	for _, e := range already {
		send(e)
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			send(e)
		}
	}
}

// emit 记录事件并广播给所有订阅者。
func (s *Server) emit(sess *session, typ, phase, msg string, ready, total int) {
	e := analyze.Event{
		At: time.Now(), Type: typ, Phase: phase,
		Message: msg, Ready: ready, Total: total,
	}
	s.mu.Lock()
	sess.events = append(sess.events, e)
	for ch := range sess.subs {
		select {
		case ch <- e:
		default: // 慢客户端丢弃，不影响演练推进
		}
	}
	s.mu.Unlock()
	log.Printf("[%s] %-5s %-8s %s", sess.id, typ, phase, msg)
}

// runExperiment 编排一次演练。真实模式连集群，mock 模式驱动模拟数据。
func (s *Server) runExperiment(sess *session, req CreateRequest) {
	if s.mockMode {
		s.runMockExperiment(sess, req)
		return
	}
	s.runRealExperiment(sess, req)
}

// runMockExperiment 用 mock 数据源跑一次完整演练，用于自测与前端联调。
func (s *Server) runMockExperiment(sess *session, req CreateRequest) {
	mq, ok := s.collector.Queryer().(*metrics.MockQueryer)
	defer func() {
		s.mu.Lock()
		sess.done = true
		res := analyze.Analyze(sess.events)
		sess.result = &res
		for ch := range sess.subs {
			close(ch)
		}
		sess.subs = make(map[chan analyze.Event]struct{})
		s.mu.Unlock()
	}()

	base, _ := s.collector.Snapshot()
	s.emit(sess, "info", "inject",
		fmt.Sprintf("[MOCK] 基线就绪 %d/%d，开始演练", base.ReadyPods, base.TotalPods),
		base.ReadyPods, base.TotalPods)
	time.Sleep(500 * time.Millisecond)

	if ok {
		mq.SetPhase("degraded")
	}
	s.emit(sess, "error", "inject", "[MOCK] 已注入 pod-kill（模拟）", base.ReadyPods, base.TotalPods)
	time.Sleep(1500 * time.Millisecond)

	deg, _ := s.collector.Snapshot()
	s.emit(sess, "warn", "detect",
		fmt.Sprintf("[MOCK] 检测到 %d 个副本不可用，可用性 %.1f%%", deg.NotReadyPods, deg.Availability()),
		deg.ReadyPods, deg.TotalPods)
	time.Sleep(2 * time.Second)

	if ok {
		mq.SetPhase("init")
	}
	rec, _ := s.collector.Snapshot()
	s.emit(sess, "ok", "recover",
		fmt.Sprintf("[MOCK] 副本全部恢复，可用性 %.1f%%", rec.Availability()),
		rec.ReadyPods, rec.TotalPods)
}

// runRealExperiment 编排一次真实演练：采集基线 → 注入 → 观察 → 恢复 → 计算。
func (s *Server) runRealExperiment(sess *session, req CreateRequest) {
	defer func() {
		s.mu.Lock()
		sess.done = true
		res := analyze.Analyze(sess.events)
		sess.result = &res
		// 关闭所有订阅 channel，让 SSE 自然结束
		for ch := range sess.subs {
			close(ch)
		}
		sess.subs = make(map[chan analyze.Event]struct{})
		s.mu.Unlock()
		// 兜底清理：防止 chaos 资源残留
		if err := s.cleanupChaos(sess); err != nil {
			log.Printf("[%s] 清理失败: %v", sess.id, err)
		}
	}()

	// 1) 基线
	base, err := s.collector.Snapshot()
	if err != nil {
		s.emit(sess, "error", "inject", "采集基线失败: "+err.Error(), 0, 0)
		return
	}
	s.emit(sess, "info", "inject",
		fmt.Sprintf("基线就绪 %d/%d，开始注入 %s", base.ReadyPods, base.TotalPods, sess.scenario),
		base.ReadyPods, base.TotalPods)

	// 2) 注入真实故障
	spec := chaos.Spec{
		Kind:          chaos.PodKill,
		Namespace:     req.Namespace,
		LabelSelector: map[string]string{"app": req.AppLabel},
		Duration:      time.Duration(req.Duration) * time.Second,
	}
	id, err := s.injector.Inject(spec)
	if err != nil {
		s.emit(sess, "error", "inject", "故障注入失败: "+err.Error(), base.ReadyPods, base.TotalPods)
		return
	}
	sess.injectID = id
	s.emit(sess, "error", "inject", "已注入 "+string(spec.Kind)+" → "+id, base.ReadyPods, base.TotalPods)

	// 3) 观察：每 2 秒采样，直到恢复或超时
	deadline := time.Now().Add(time.Duration(req.Duration)*time.Second + 45*time.Second)
	detected := false
	recovered := false
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		snap, err := s.collector.Snapshot()
		if err != nil {
			s.emit(sess, "warn", "detect", "采样失败: "+err.Error(), 0, 0)
			continue
		}
		if !detected && snap.NotReadyPods > 0 {
			detected = true
			s.emit(sess, "warn", "detect",
				fmt.Sprintf("检测到 %d 个副本不可用，可用性 %.1f%%", snap.NotReadyPods, snap.Availability()),
				snap.ReadyPods, snap.TotalPods)
		}
		if detected && snap.NotReadyPods == 0 {
			recovered = true
			s.emit(sess, "ok", "recover",
				fmt.Sprintf("所有副本恢复就绪，可用性 %.1f%%", snap.Availability()),
				snap.ReadyPods, snap.TotalPods)
			break
		}
		if detected {
			s.emit(sess, "info", "mitigate",
				fmt.Sprintf("等待自愈：就绪 %d/%d", snap.ReadyPods, snap.TotalPods),
				snap.ReadyPods, snap.TotalPods)
		}
	}
	if !detected {
		s.emit(sess, "warn", "detect", "观察窗口内未检测到副本异常（可能被调度器快速掩盖）",
			base.ReadyPods, base.TotalPods)
	}
	if !recovered {
		s.emit(sess, "warn", "recover", "观察窗口结束仍未完全恢复", 0, 0)
	}
}

func (s *Server) cleanupChaos(sess *session) error {
	if sess.injectID == "" {
		return nil
	}
	return s.injector.Cleanup(s.kube.Config().Namespace, sess.injectID)
}
