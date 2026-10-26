// Package netproxy follows explicit process proxy settings, then Windows Internet Settings.
package netproxy

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

func Proxy(r *http.Request) (*url.URL, error) {
	host := r.URL.Hostname()
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return nil, nil
	}
	server, bypass := systemSettings()
	return resolve(r, httpproxy.FromEnvironment(), server, bypass)
}

func resolve(r *http.Request, env *httpproxy.Config, server, bypass string) (*url.URL, error) {
	// NO_PROXY is only a bypass list, not an actual proxy configuration.
	if env.HTTPProxy == "" && env.HTTPSProxy == "" {
		p, err := fromSettings(r, server, bypass)
		if err != nil || p == nil {
			return p, err
		}
		copy := *env
		copy.HTTPProxy, copy.HTTPSProxy = p.String(), p.String()
		env = &copy
	}
	return env.ProxyFunc()(r.URL)
}

func NewTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = Proxy
	return t
}

func fromSettings(r *http.Request, server, bypass string) (*url.URL, error) {
	host := strings.ToLower(r.URL.Hostname())
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return nil, nil
	}
	for _, pattern := range strings.Split(strings.ToLower(bypass), ";") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "<local>" && !strings.Contains(host, ".") {
			return nil, nil
		}
		if match, _ := path.Match(pattern, host); pattern != "" && match {
			return nil, nil
		}
	}
	selected := strings.TrimSpace(server)
	if strings.Contains(server, "=") {
		selected = ""
		for _, part := range strings.Split(server, ";") {
			key, value, ok := strings.Cut(part, "=")
			if ok && strings.TrimSpace(key) == r.URL.Scheme {
				selected = strings.TrimSpace(value)
				break
			}
		}
	}
	if selected == "" {
		return nil, nil
	}
	if !strings.Contains(selected, "://") {
		selected = "http://" + selected
	}
	return url.Parse(selected)
}
