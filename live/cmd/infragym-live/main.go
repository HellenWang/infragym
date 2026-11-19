// infragym-live 是 InfraGym 真实集群模式的 control plane。
// 它连接 kubeconfig 指定的集群，用 chaos-mesh 注入故障，
// 从 Prometheus 读取真实指标，通过 SSE 把事件推给前端。
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hellenwang/infragym/live/internal/kube"
	"github.com/hellenwang/infragym/live/internal/metrics"
	"github.com/hellenwang/infragym/live/internal/server"
)

func main() {
	addr := flag.String("addr", ":8088", "监听地址")
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig 路径，默认读 $KUBECONFIG 或 ~/.kube/config")
	promURL := flag.String("prometheus", "http://127.0.0.1:9090", "Prometheus 地址")
	namespace := flag.String("namespace", "infragym-demo", "演练命名空间")
	appLabel := flag.String("app-label", "infragym-demo", "目标 workload 的 app 标签")
	mock := flag.Bool("mock", false, "使用模拟数据源（无需集群，用于自测）")
	flag.Parse()

	var srv *server.Server
	if *mock {
		log.Println("⚠ MOCK 模式：使用模拟数据，不会连接真实集群")
		q := metrics.NewMockQueryer()
		srv = server.New(nil, q, *namespace, *appLabel) // mock 下 kube 为 nil
	} else {
		cfg, err := kube.LoadConfig(*kubeconfig)
		if err != nil {
			log.Fatalf("加载 kubeconfig 失败: %v", err)
		}
		k, err := kube.NewClient(cfg)
		if err != nil {
			log.Fatalf("创建 K8s 客户端失败: %v", err)
		}
		ver, err := k.Version()
		if err != nil {
			log.Fatalf("无法连接集群（请确认 kubeconfig 与网络）: %v", err)
		}
		log.Printf("已连接集群 %s，namespace=%s", ver, cfg.Namespace)
		srv = server.New(k, metrics.NewClient(*promURL), *namespace, *appLabel)
	}

	handler := srv.Routes()

	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("infragym-live 监听 %s", *addr)
	log.Fatal(server.ListenAndServe())
}

// 确保 os 被使用（Windows 下 kubeconfig 路径处理预留）
var _ = os.Getenv
