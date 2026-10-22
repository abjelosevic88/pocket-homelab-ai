package sdk

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

//go:embed ask-widget.js
var askWidget []byte

// AskWidget serves the shared contextual launcher. The public issuer is learned
// at Core registration; internal API addresses never have to be hardcoded in apps.
func (c *Client) AskWidget(app string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := c.OIDC().Issuer
		if base == "" && c.Configured() {
			base = c.BaseURL()
		}
		ServeAskWidget(w, r, app, base)
	}
}
func ServeAskWidget(w http.ResponseWriter, r *http.Request, app, coreURL string) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	base, err := url.Parse(coreURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") {
		w.Write([]byte("/* Pocket Ask is available when Core is connected. */"))
		return
	}
	config, _ := json.Marshal(map[string]string{"app": app, "core": strings.TrimRight(coreURL, "/")})
	w.Write([]byte("(()=>{const config="))
	w.Write(config)
	w.Write([]byte(";\n"))
	w.Write(askWidget)
	w.Write([]byte("\n})();"))
}
