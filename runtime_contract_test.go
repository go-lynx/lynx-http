package http

import (
	"testing"
	"time"

	"github.com/go-lynx/lynx-http/conf"
	"github.com/go-lynx/lynx/plugins"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestHTTPRuntimeContract_LocalBootstrapLifecycle(t *testing.T) {
	base := plugins.NewSimpleRuntime()
	rt := base.WithPluginContext(pluginName)

	plugin := NewServiceHttp()
	plugin.rt = rt
	plugin.conf = &conf.Http{
		Network: "tcp",
		Addr:    "127.0.0.1:0",
		Timeout: durationpb.New(time.Second),
		Monitoring: &conf.MonitoringConfig{
			EnableMetrics: false,
			MetricsPath:   "/metrics",
			HealthPath:    "/health",
		},
	}
	plugin.setDefaultConfig()

	if err := plugin.StartupTasks(); err != nil {
		t.Fatalf("StartupTasks failed: %v", err)
	}

	alias, err := base.GetSharedResource(sharedPluginResourceName)
	if err != nil {
		t.Fatalf("shared plugin alias: %v", err)
	}
	if alias != plugin {
		t.Fatalf("unexpected shared plugin alias: got %T want %T", alias, plugin)
	}

	ready, err := base.GetSharedResource(sharedReadinessResourceName)
	if err != nil {
		t.Fatalf("shared readiness: %v", err)
	}
	if ready != true {
		t.Fatalf("expected shared readiness true, got %#v", ready)
	}

	healthy, err := base.GetSharedResource(sharedHealthResourceName)
	if err != nil {
		t.Fatalf("shared health: %v", err)
	}
	if healthy != true {
		t.Fatalf("expected shared health true, got %#v", healthy)
	}

	privateReady, err := rt.GetPrivateResource(privateReadinessResourceName)
	if err != nil {
		t.Fatalf("private readiness: %v", err)
	}
	if privateReady != true {
		t.Fatalf("expected private readiness true, got %#v", privateReady)
	}

	privateHealth, err := rt.GetPrivateResource(privateHealthResourceName)
	if err != nil {
		t.Fatalf("private health: %v", err)
	}
	if privateHealth != true {
		t.Fatalf("expected private health true, got %#v", privateHealth)
	}

	if _, err := rt.GetPrivateResource("config"); err != nil {
		t.Fatalf("private config resource missing: %v", err)
	}
	if _, err := rt.GetPrivateResource("server"); err != nil {
		t.Fatalf("private server resource missing: %v", err)
	}

	if err := plugin.CleanupTasks(); err != nil {
		t.Fatalf("CleanupTasks failed: %v", err)
	}

	ready, err = base.GetSharedResource(sharedReadinessResourceName)
	if err != nil {
		t.Fatalf("shared readiness after cleanup: %v", err)
	}
	if ready != false {
		t.Fatalf("expected shared readiness false after cleanup, got %#v", ready)
	}

	healthy, err = base.GetSharedResource(sharedHealthResourceName)
	if err != nil {
		t.Fatalf("shared health after cleanup: %v", err)
	}
	if healthy != false {
		t.Fatalf("expected shared health false after cleanup, got %#v", healthy)
	}
}
