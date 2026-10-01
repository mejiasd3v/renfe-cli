package main

import (
	"strings"
	"testing"
)

func TestPickPassengers(t *testing.T) {
	file := passengerFile{Passengers: []passenger{
		{ID: "me", Type: "adult", Name: "Ana", Surname1: "García", DocumentType: "DNI", Document: "00000000T", Email: "ana@example.com", Phone: "+34600000000"},
		{ID: "kid", Type: "child", Name: "Leo", Surname1: "García", DocumentType: "dni", Document: "00000001R"},
		{ID: "baby", Type: "infant", Name: "Mia", Surname1: "García"},
		{ID: "nomail", Type: "adult", Name: "Luis", Surname1: "Pérez", DocumentType: "NIE", Document: "X0000000T", Phone: "+34600000001"},
		{ID: "badphone", Type: "adult", Name: "Eva", Surname1: "Ruiz", DocumentType: "passport", Document: "P1", Email: "eva@example.com", Phone: "600000000"},
	}}
	for _, tc := range []struct {
		ids     []string
		wantErr string
	}{
		{ids: []string{"me", "kid", "baby", "nomail"}}, // only the buyer needs email and phone
		{ids: []string{"kid", "me"}, wantErr: "must be an adult"},
		{ids: []string{"nomail"}, wantErr: "buyer needs an email"},
		{ids: []string{"badphone"}, wantErr: "international format"},
		{ids: []string{"me", "me"}, wantErr: "listed twice"},
		{ids: []string{"me", "ghost"}, wantErr: `no passenger with id "ghost"`},
	} {
		got, err := pickPassengers(file, tc.ids)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%v: got %v, want error containing %q", tc.ids, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%v: %v", tc.ids, err)
		}
		if p := countParty(got); p != (party{Adults: 2, Children: 1, Infants: 1}) || got[0].ID != "me" {
			t.Errorf("%v: counts %+v, first %s", tc.ids, p, got[0].ID)
		}
	}
}
