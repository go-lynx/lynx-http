package http

import "github.com/go-lynx/lynx"

func currentLynxApp() *lynx.LynxApp {
	return lynx.Lynx()
}

func currentLynxName() string {
	if app := currentLynxApp(); app != nil {
		return app.Name()
	}
	return ""
}
