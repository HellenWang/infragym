package analyze

import (
	"testing"
	"time"
)

func TestAnalyzeFullLifecycle(t *testing.T) {
	base := time.Now()
	total := 6
	events := []Event{
		{At: base, Type: "info", Phase: "inject", Message: "基线", Ready: 6, Total: total},
		{At: base.Add(1 * time.Second), Type: "error", Phase: "inject", Message: "注入", Ready: 6, Total: total},
		{At: base.Add(4 * time.Second), Type: "warn", Phase: "detect", Message: "检测到异常", Ready: 4, Total: total},
		{At: base.Add(6 * time.Second), Type: "info", Phase: "mitigate", Message: "自愈中", Ready: 5, Total: total},
		{At: base.Add(11 * time.Second), Type: "ok", Phase: "recover", Message: "恢复", Ready: 6, Total: total},
	}

	r := Analyze(events)
	if !r.Detected {
		t.Error("应判定为已检测")
	}
	if !r.Recovered {
		t.Error("应判定为已恢复")
	}
	if r.DetectLatency != 3*time.Second {
		t.Errorf("检测延迟 = %v, want 3s", r.DetectLatency)
	}
	if r.MTTR != 10*time.Second {
		t.Errorf("MTTR = %v, want 10s", r.MTTR)
	}
	if r.ImpactPods != 2 {
		t.Errorf("影响副本 = %d, want 2", r.ImpactPods)
	}
	if r.MinReady != 4 {
		t.Errorf("最小就绪 = %d, want 4", r.MinReady)
	}
	if r.Events != 5 {
		t.Errorf("事件数 = %d, want 5", r.Events)
	}
}

func TestAnalyzeNeverRecovered(t *testing.T) {
	base := time.Now()
	events := []Event{
		{At: base, Phase: "inject", Ready: 6, Total: 6},
		{At: base.Add(2 * time.Second), Phase: "detect", Ready: 3, Total: 6},
		{At: base.Add(9 * time.Second), Phase: "mitigate", Ready: 3, Total: 6},
	}
	r := Analyze(events)
	if r.Recovered {
		t.Error("不应判定为已恢复")
	}
	if r.MTTR != 0 {
		t.Errorf("未恢复时 MTTR 应为 0, got %v", r.MTTR)
	}
	if r.ImpactPods != 3 {
		t.Errorf("影响副本 = %d, want 3", r.ImpactPods)
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	r := Analyze(nil)
	if r.Detected || r.Recovered || r.Events != 0 {
		t.Errorf("空事件序列应返回零值, got %+v", r)
	}
}

func TestAnalyzeRecoveryByFinalReady(t *testing.T) {
	// 没有显式 recover 事件，但最后就绪数回到基线，应判定恢复
	base := time.Now()
	events := []Event{
		{At: base, Phase: "inject", Ready: 6, Total: 6},
		{At: base.Add(2 * time.Second), Phase: "detect", Ready: 4, Total: 6},
		{At: base.Add(5 * time.Second), Phase: "mitigate", Ready: 6, Total: 6},
	}
	r := Analyze(events)
	if !r.Recovered {
		t.Error("最终就绪回到基线应判定为已恢复")
	}
}
