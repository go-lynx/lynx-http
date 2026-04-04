package http

import "github.com/go-lynx/lynx/log"

const (
	sharedPluginResourceName     = pluginName + ".plugin"
	sharedReadinessResourceName  = pluginName + ".readiness"
	sharedHealthResourceName     = pluginName + ".health"
	privateReadinessResourceName = "readiness"
	privateHealthResourceName    = "health"
)

func (h *ServiceHttp) registerRuntimePluginAlias() {
	if h == nil || h.rt == nil {
		return
	}
	if err := h.rt.RegisterSharedResource(sharedPluginResourceName, h); err != nil {
		log.Warnf("failed to register http shared plugin alias: %v", err)
	}
}

func (h *ServiceHttp) publishRuntimeContract(ready, healthy bool) {
	if h == nil || h.rt == nil {
		return
	}
	for _, item := range []struct {
		name  string
		value any
	}{
		{name: sharedReadinessResourceName, value: ready},
		{name: sharedHealthResourceName, value: healthy},
	} {
		if err := h.rt.RegisterSharedResource(item.name, item.value); err != nil {
			log.Warnf("failed to register http shared runtime contract %s: %v", item.name, err)
		}
	}
	if err := h.rt.RegisterPrivateResource(privateReadinessResourceName, ready); err != nil {
		log.Warnf("failed to register http private readiness resource: %v", err)
	}
	if err := h.rt.RegisterPrivateResource(privateHealthResourceName, healthy); err != nil {
		log.Warnf("failed to register http private health resource: %v", err)
	}
}
