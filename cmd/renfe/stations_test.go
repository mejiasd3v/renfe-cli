package main

import (
	"io"
	"testing"
)

func TestStationParsingAndMatching(t *testing.T) {
	// renfe.com serves this file as Latin-1: 0xCD is "Í", 0xC1 is "Á".
	body := []byte("var estacionesEstatico=[" +
		`{"cdgoEstacion":"MADRI","nmroPrioridad":1,"desgEstacion":"MADRID (TODAS)","clave":"0071,MADRI,null"},` +
		`{"cdgoEstacion":"60000","nmroPrioridad":2,"desgEstacion":"MADRID-PUERTA DE ATOCHA-ALMUDENA GRANDES","clave":"0071,60000,00600"},` +
		`{"cdgoEstacion":"17000","nmroPrioridad":5,"desgEstacion":"MADRID-CHAMART` + "\xcd" + `N-CLARA CAMPOAMOR","clave":"0071,17000,17000"},` +
		`{"cdgoEstacion":"71801","nmroPrioridad":4,"desgEstacion":"BARCELONA-SANTS","clave":"0071,71801,71801"}` +
		"];\nvar estacionesDestacada=[" +
		`{"cdgoEstacion":"51405","nmroPrioridad":9999,"desgEstacion":"C` + "\xc1" + `DIZ","clave":"0071,51405,51405"},` +
		`{"cdgoEstacion":"71801","nmroPrioridad":9999,"desgEstacion":"BARCELONA-SANTS","clave":"0071,71801,71801"}` +
		"];")
	stations, err := parseStations(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 5 {
		t.Fatalf("got %d stations, want 5 (featured duplicates merged)", len(stations))
	}
	for _, tc := range []struct{ query, code string }{
		{"madrid", "MADRI"},              // most prominent of several matches
		{"Chamartín", "17000"},           // accents folded both ways
		{"barcelona sants", "71801"},     // all words must match
		{"Cadiz", "51405"},               // featured-only station with Latin-1 name
		{"60000", "60000"},               // station code
		{"madrid atocha almud", "60000"}, // word prefixes
		{"MADRID (TODAS)", "MADRI"},      // exact name
	} {
		got, err := resolveStation(stations, tc.query, io.Discard)
		if err != nil || got.Code != tc.code {
			t.Errorf("%q: got %s %v, want %s", tc.query, got.Code, err, tc.code)
		}
	}
	if s, _ := resolveStation(stations, "Cadiz", io.Discard); s.Name != "CÁDIZ" || s.Key != "0071,51405,51405" {
		t.Errorf("Cádiz decoded as %+v", s)
	}
	if _, err := resolveStation(stations, "sevilla", io.Discard); err == nil {
		t.Error("expected no match for sevilla")
	}
}
