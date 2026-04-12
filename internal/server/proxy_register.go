package server

import (
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

// handleNewProxy registers the proxy named in m with the router and replies
// to frpc on sendCh. Only HTTP proxies are supported in drps; any other type
// is rejected immediately.
//
// Registration is all-or-nothing per NewProxy message: if any per-location
// Add fails, every route added for this proxy is rolled back before the
// error is returned.
func (h *Handler) handleNewProxy(sendCh chan msg.Message, m *msg.NewProxy, runID string, registeredProxies map[string]struct{}) {
	if m.ProxyType != "http" {
		sendCh <- &msg.NewProxyResp{
			ProxyName: m.ProxyName,
			Error:     "only http proxy type is supported",
		}
		return
	}

	if h.Router != nil {
		domains := m.CustomDomains

		// Track the names we have already registered so we can roll back
		// on a partial failure. With a single proxy name this is either
		// empty or {m.ProxyName}, but the loop keeps the shape explicit.
		var registered []string
		for _, domain := range domains {
			cfg := &router.RouteConfig{
				Domain:            domain,
				Location:          "/",
				ProxyName:         m.ProxyName,
				RunID:             runID,
				UseEncryption:     m.UseEncryption,
				UseCompression:    m.UseCompression,
				HTTPUser:          m.HTTPUser,
				HTTPPwd:           m.HTTPPwd,
				HostHeaderRewrite: m.HostHeaderRewrite,
				Headers:           m.Headers,
				ResponseHeaders:   m.ResponseHeaders,
			}

			if len(m.Locations) > 0 {
				for _, loc := range m.Locations {
					locCfg := *cfg
					locCfg.Location = loc
					if err := h.Router.Add(&locCfg); err != nil {
						for _, name := range registered {
							h.Router.Remove(name)
						}
						sendCh <- &msg.NewProxyResp{
							ProxyName: m.ProxyName,
							Error:     err.Error(),
						}
						return
					}
				}
			} else {
				if err := h.Router.Add(cfg); err != nil {
					h.Router.Remove(m.ProxyName)
					sendCh <- &msg.NewProxyResp{
						ProxyName: m.ProxyName,
						Error:     err.Error(),
					}
					return
				}
			}
			registered = append(registered, m.ProxyName)
		}
		registeredProxies[m.ProxyName] = struct{}{}
	}

	sendCh <- &msg.NewProxyResp{
		ProxyName:  m.ProxyName,
		RemoteAddr: ":80",
	}
}
