package main

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// cookieJar is a minimal RFC 6265 jar for renfe.com that, unlike net/http/cookiejar,
// keeps cookie attributes so the session can be handed to a browser.
type cookieJar struct {
	mu      sync.Mutex
	cookies []jarCookie
}

type jarCookie struct {
	http.Cookie
	hostOnly bool
}

func (j *cookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.ToLower(u.Hostname())
	for _, c := range cookies {
		jc := jarCookie{Cookie: *c}
		jc.Domain = strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if jc.Domain == "" {
			jc.Domain, jc.hostOnly = host, true
		}
		if !domainMatch(host, jc.Domain) || !(jc.Domain == "renfe.com" || strings.HasSuffix(jc.Domain, ".renfe.com")) {
			continue
		}
		if jc.Path == "" || jc.Path[0] != '/' {
			jc.Path = defaultPath(u.Path)
		}
		if c.MaxAge > 0 {
			jc.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
		}
		j.cookies = slices.DeleteFunc(j.cookies, func(old jarCookie) bool {
			return old.Name == jc.Name && old.Domain == jc.Domain && old.Path == jc.Path
		})
		expired := c.MaxAge < 0 || (!jc.Expires.IsZero() && jc.Expires.Before(time.Now()))
		if !expired {
			j.cookies = append(j.cookies, jc)
		}
	}
}

func (j *cookieJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.ToLower(u.Hostname())
	path := u.Path
	if path == "" {
		path = "/"
	}
	var out []*http.Cookie
	for _, c := range j.cookies {
		if (c.hostOnly && host != c.Domain) || !domainMatch(host, c.Domain) || (c.Secure && u.Scheme != "https") {
			continue
		}
		if path != c.Path && !(strings.HasPrefix(path, c.Path) && (strings.HasSuffix(c.Path, "/") || path[len(c.Path)] == '/')) {
			continue
		}
		if !c.Expires.IsZero() && c.Expires.Before(time.Now()) {
			continue
		}
		out = append(out, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return out
}

func (j *cookieJar) all() []jarCookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]jarCookie(nil), j.cookies...)
}

func domainMatch(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func defaultPath(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}
