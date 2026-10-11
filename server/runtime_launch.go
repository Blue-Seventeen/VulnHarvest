package server

import "github.com/Autumn-27/artex/db"

// MCPLaunchOverlay is session-only. 5b supplies the retained proxy listener's
// actual address and CA at launch; these values never mutate saved MCP records.
type MCPLaunchOverlay struct {
	ProxyAddr string
	CACert    string
	Env       map[string]string
}

func mcpLaunchValues(server *db.MCPServer, overlay *MCPLaunchOverlay) ([]string, map[string]string) {
	args := jsonStrSlice(server.Args)
	env := jsonStrMap(server.Env)
	if env == nil {
		env = map[string]string{}
	}
	if overlay == nil {
		return args, env
	}
	for k, v := range overlay.Env {
		env[k] = v
	}
	if server.Name == browserMCPName {
		args = stripProxyArgs(args)
		delete(env, "NODE_EXTRA_CA_CERTS")
		if overlay.ProxyAddr != "" {
			args = append(args, "--proxy-server", overlay.ProxyAddr)
		}
	}
	if overlay.ProxyAddr != "" {
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy"} {
			env[k] = overlay.ProxyAddr
		}
		env["NODE_USE_ENV_PROXY"] = "1"
	}
	if overlay.CACert != "" {
		for _, k := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS"} {
			env[k] = overlay.CACert
		}
	}
	return args, env
}

func (s *Server) runtimeMCPOverlay(_ *db.MCPServer) MCPLaunchOverlay {
	if s == nil || s.m == nil {
		return MCPLaunchOverlay{}
	}
	return MCPLaunchOverlay{ProxyAddr: s.m.ProxyAddr(), CACert: s.m.ProxyCACert()}
}
