package main

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestCookieJarScopesAndHandoffAttributes(t *testing.T) {
	jar := &cookieJar{}
	search := &url.URL{Scheme: "https", Host: "venta.renfe.com", Path: "/vol/buscarTren.do"}
	jar.SetCookies(search, []*http.Cookie{
		{Name: "JSESSIONID", Value: "s1", Path: "/", HttpOnly: true},
		{Name: "tipoUsuario", Value: "N"}, // no Path: defaults to /vol
		{Name: "TS01", Value: "t", Domain: ".venta.renfe.com", Path: "/"},
		{Name: "tracker", Value: "x", Domain: ".example.com", Path: "/"},
	})
	names := func(u string) []string {
		parsed, _ := url.Parse(u)
		var out []string
		for _, c := range jar.Cookies(parsed) {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names("https://venta.renfe.com/vol/dwr/call"); !reflect.DeepEqual(got, []string{"JSESSIONID", "tipoUsuario", "TS01"}) {
		t.Fatalf("cookies for /vol/dwr: %v", got)
	}
	if got := names("https://venta.renfe.com/volx"); !reflect.DeepEqual(got, []string{"JSESSIONID", "TS01"}) {
		t.Fatalf("cookies for /volx: %v", got)
	}
	if got := names("https://www.renfe.com/"); got != nil {
		t.Fatalf("host-only and venta-scoped cookies leaked to www: %v", got)
	}
	jar.SetCookies(search, []*http.Cookie{{Name: "JSESSIONID", Path: "/", MaxAge: -1}})
	var handoff []string
	for _, c := range jar.all() {
		handoff = append(handoff, c.Name+" "+c.Domain+c.Path+" hostOnly="+map[bool]string{true: "yes", false: "no"}[c.hostOnly])
	}
	if want := []string{"tipoUsuario venta.renfe.com/vol hostOnly=yes", "TS01 venta.renfe.com/ hostOnly=no"}; !reflect.DeepEqual(handoff, want) {
		t.Fatalf("handoff cookies: %v, want %v", handoff, want)
	}
}
