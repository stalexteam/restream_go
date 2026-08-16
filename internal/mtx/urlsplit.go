package mtx

import "strings"

// Вузький ручний urlsplit (як egress/output_url.go E1): лише
// scheme/hostname/port. \t\r\n вирізаються звідусіль, C0-control/space —
// лише з лівого краю, як python urllib.parse.

const whatwgC0OrSpace = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
	"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x20"

const schemeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+-."

var unsafeURLBytes = strings.NewReplacer("\t", "", "\r", "", "\n", "")

func splitSchemeNetloc(raw string) (scheme, netloc string) {
	s := unsafeURLBytes.Replace(raw)
	s = strings.TrimLeft(s, whatwgC0OrSpace)

	rest := s
	if i := strings.IndexByte(s, ':'); i > 0 && isASCIIAlpha(s[0]) && onlySchemeChars(s[:i]) {
		scheme = strings.ToLower(s[:i])
		rest = s[i+1:]
	}
	if strings.HasPrefix(rest, "//") {
		netloc = splitNetloc(rest[2:])
	}
	return scheme, netloc
}

func onlySchemeChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(schemeChars, rune(s[i])) {
			return false
		}
	}
	return true
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func splitNetloc(s string) string {
	delim := len(s)
	for _, c := range "/?#" {
		if i := strings.IndexRune(s, c); i >= 0 && i < delim {
			delim = i
		}
	}
	return s[:delim]
}

// hostinfo — python SplitResult._hostinfo: сирі (host, port) до
// lower-case/int-парсингу, userinfo відкинуто, IPv6-дужки зняті.
func hostinfo(netloc string) (host, port string) {
	authority := netloc
	if i := strings.LastIndex(netloc, "@"); i >= 0 {
		authority = netloc[i+1:]
	}
	if i := strings.IndexByte(authority, '['); i >= 0 {
		bracketed := authority[i+1:]
		hostPart, rest, _ := strings.Cut(bracketed, "]")
		_, portPart, _ := strings.Cut(rest, ":")
		return hostPart, portPart
	}
	host, port, _ = strings.Cut(authority, ":")
	return host, port
}

// pyHostname — SplitResult.hostname: "" -> (,,false); інакше lower-case,
// зона %tESt після '%' лишається як є.
func pyHostname(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	base, zone, hasZone := strings.Cut(raw, "%")
	host := strings.ToLower(base)
	if hasZone {
		host += "%" + zone
	}
	return host, true
}

// pyPort — SplitResult.port: "" = немає порту; лише ASCII-цифри 0-65535
// валідні, інакше ok=false (python тут кинув би ValueError, нотатка).
func pyPort(raw string) (n int, has bool, ok bool) {
	if raw == "" {
		return 0, false, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false, false
		}
	}
	v := 0
	for i := 0; i < len(raw); i++ {
		v = v*10 + int(raw[i]-'0')
		if v > 65535+1 { // досить, щоб гарантовано вийти за межу нижче
			return 0, false, false
		}
	}
	if v > 65535 {
		return 0, false, false
	}
	return v, true, true
}
