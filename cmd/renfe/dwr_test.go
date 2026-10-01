package main

import (
	"strings"
	"testing"
)

func TestDWRBodyMatchesEngineEncoding(t *testing.T) {
	selection := []field{{"ida", []any{2, "N1010", false, "3#03063"}}, {"idCompra", "_aB1c"}}
	got := dwrBody("trainEnlacesManager", "setTrenEnlaceSeleccionado", []any{selection}, 4, "/vol/buscarTrenEnlaces.do", "SID/page")
	want := strings.Join([]string{
		"callCount=1", "nextReverseAjaxIndex=0",
		"c0-scriptName=trainEnlacesManager", "c0-methodName=setTrenEnlaceSeleccionado", "c0-id=0",
		"c0-e2=number:2", "c0-e3=string:N1010", "c0-e4=boolean:false", "c0-e5=string:3%2303063",
		"c0-e1=array:[reference:c0-e2,reference:c0-e3,reference:c0-e4,reference:c0-e5]",
		"c0-e6=string:_aB1c",
		"c0-param0=Object_Object:{ida:reference:c0-e1, idCompra:reference:c0-e6}",
		"batchId=4", "instanceId=0", "page=%2Fvol%2FbuscarTrenEnlaces.do", "scriptSessionId=SID/page", "windowName=",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("body mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestDWRReplyErrors(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"exception", `//#DWR-REPLY
r.handleException("3","0",{javaClassName:"java.lang.Exception",msgError:"La sesi\u00F3n ha caducado",cdgoError:"U014"});`, "Renfe error U014: La sesión ha caducado"},
		{"batch exception", `//#DWR-REPLY
r.handleBatchException({name:"java.lang.SecurityException",message:"CSRF Security Error"});`, "Renfe error: CSRF Security Error"},
	} {
		if _, err := dwrReply([]byte(tc.body)); err == nil || err.Error() != tc.want {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}
