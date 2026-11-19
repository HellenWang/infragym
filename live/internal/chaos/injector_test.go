package chaos

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hellenwang/infragym/live/internal/kube"
)

func testClient(t *testing.T, handler http.HandlerFunc) *kube.Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	cfg := &kube.Config{
		Server:    srv.URL,
		Namespace: "infragym-demo",
		Token:     "test-token",
		CAData:    nil, // 无 CA 时 NewClient 走 InsecureSkipVerify
	}
	c, err := kube.NewClient(cfg)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	return c
}

func TestInjectPodKill(t *testing.T) {
	var captured crd
	var gotPath, gotMethod, gotAuth string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{}`)
	})

	inj := NewInjector(c)
	name, err := inj.Inject(Spec{
		Kind:          PodKill,
		LabelSelector: map[string]string{"app": "infragym-demo"},
		Duration:      30 * time.Second,
	})
	if err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if !strings.Contains(gotPath, "/podchaos") {
		t.Errorf("path = %q, 应包含 podchaos", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if captured.Kind != "PodChaos" || captured.APIVersion != "chaos-mesh.org/v1alpha1" {
		t.Errorf("CRD 类型错误: %+v", captured)
	}
	if !strings.HasPrefix(name, "podchaos/ig-PodChaos") {
		t.Errorf("返回的资源名异常: %q", name)
	}
	if captured.Metadata.Labels["app.kubernetes.io/managed-by"] != "infragym" {
		t.Error("缺少 managed-by 标签，兜底清理会失效")
	}
}

func TestInjectRequiresSelector(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("没有 selector 时不应发起请求")
	})
	inj := NewInjector(c)
	if _, err := inj.Inject(Spec{Kind: PodKill}); err == nil {
		t.Error("空 LabelSelector 应报错（安全约束）")
	}
}

func TestInjectNetworkDelay(t *testing.T) {
	var captured crd
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		w.WriteHeader(http.StatusCreated)
	})
	inj := NewInjector(c)
	_, err := inj.Inject(Spec{
		Kind:          NetDelay,
		LabelSelector: map[string]string{"app": "demo"},
		Latency:       "200ms",
	})
	if err != nil {
		t.Fatalf("注入网络延迟失败: %v", err)
	}
	if captured.Kind != "NetworkChaos" {
		t.Errorf("kind = %q, want NetworkChaos", captured.Kind)
	}
}
