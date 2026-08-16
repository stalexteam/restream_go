// Package egress будує push-URL і argv зовнішніх процесів для виходів:
// (_build_out_args, _build_srt_push_transport_args).
package egress

import "strings"

// BuildPushURL — server+key -> push-URL;
// порожній key означає, що server уже повний URL.
func BuildPushURL(server, key string) string {
	server = strings.TrimSpace(server)
	key = strings.TrimSpace(key)
	if server == "" {
		return ""
	}
	url := server
	if key != "" {
		url = strings.TrimRight(server, "/") + "/" + strings.TrimLeft(key, "/")
	}
	return normalizeIVS(url)
}

// BuildSRTURL — srt-URL із параметрами. Значення НЕ URL-енкодяться:
// площадка порівнює їх посимвольно.
func BuildSRTURL(server, streamid, passphrase string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	var params []string
	if strings.TrimSpace(streamid) != "" {
		params = append(params, "streamid="+strings.TrimSpace(streamid))
	}
	if strings.TrimSpace(passphrase) != "" {
		params = append(params, "passphrase="+strings.TrimSpace(passphrase))
	}
	if len(params) == 0 {
		return server
	}
	joiner := "?"
	if strings.Contains(server, "?") {
		joiner = "&"
	}
	return server + joiner + strings.Join(params, "&")
}

// normalizeIVS — *.live-video.net (rtmps)
// отримує:443 і /app, ідемпотентно; інші URL повертаються без змін.
func normalizeIVS(url string) string {
	scheme, netloc, path, query, fragment := splitURL(url)
	host := hostnameOf(netloc)
	if scheme != "rtmps" || !(host == "live-video.net" || strings.HasSuffix(host, ".live-video.net")) {
		return url
	}
	newNetloc := netloc
	if !hasPort(netloc) {
		newNetloc = host + ":443"
	}
	if path != "/app" && !strings.HasPrefix(path, "/app/") {
		if strings.HasPrefix(path, "/") {
			path = "/app" + path
		} else {
			path = "/app/" + path
		}
	}
	return joinURL(scheme, newNetloc, path, query, fragment)
}

// splitURL — вузький розбір URL (без userinfo/IPv6 —
// не трапляються тут); fragment розбирається перед query.
func splitURL(u string) (scheme, netloc, path, query, fragment string) {
	rest := u
	if i := strings.IndexByte(rest, ':'); i > 0 {
		ok := true
		for j := 0; j < i; j++ {
			if !isSchemeChar(rest[j]) {
				ok = false
				break
			}
		}
		if ok {
			scheme = strings.ToLower(rest[:i])
			rest = rest[i+1:]
		}
	}
	if strings.HasPrefix(rest, "//") {
		tail := rest[2:]
		if delim := strings.IndexAny(tail, "/?#"); delim < 0 {
			netloc, rest = tail, ""
		} else {
			netloc, rest = tail[:delim], tail[delim:]
		}
	}
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		fragment, rest = rest[i+1:], rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query, rest = rest[i+1:], rest[:i]
	}
	path = rest
	return
}

func isSchemeChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
}

// hostnameOf — порт SplitResult.hostname: host завжди lower-caситься.
func hostnameOf(netloc string) string {
	hostinfo := netloc
	if i := strings.LastIndexByte(netloc, '@'); i >= 0 {
		hostinfo = netloc[i+1:]
	}
	host := hostinfo
	if i := strings.IndexByte(hostinfo, ':'); i >= 0 {
		host = hostinfo[:i]
	}
	return strings.ToLower(host)
}

// hasPort — порожній рядок після ':' означає "порту немає" (SplitResult).
func hasPort(netloc string) bool {
	hostinfo := netloc
	if i := strings.LastIndexByte(netloc, '@'); i >= 0 {
		hostinfo = netloc[i+1:]
	}
	i := strings.IndexByte(hostinfo, ':')
	return i >= 0 && i < len(hostinfo)-1
}

// joinURL — збірка URL назад для гілки netloc!="".
func joinURL(scheme, netloc, path, query, fragment string) string {
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := "//" + netloc + path
	if scheme != "" {
		url = scheme + ":" + url
	}
	if query != "" {
		url += "?" + query
	}
	if fragment != "" {
		url += "#" + fragment
	}
	return url
}
