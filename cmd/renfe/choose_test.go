package main

import (
	"strings"
	"testing"
)

func TestChooseTrainAndFare(t *testing.T) {
	ave := func(id int, dep string, numbers ...string) train {
		t := train{ID: id, Departure: dep}
		for _, n := range numbers {
			t.Legs = append(t.Legs, leg{Train: n})
		}
		return t
	}
	direct := ave(2, "06:27", "03063")
	direct.Fares = []fare{
		{Code: "N1010", Name: "Básico", PriceEUR: 49.8},
		{Code: "N2010", Name: "Elige", PriceEUR: 54.8},
		{Code: "N3010", Name: "Elige Confort", PriceEUR: 59.6},
		{Code: "H1010", Name: "Plaza H", PriceEUR: 20, WheelchairOnly: true},
	}
	viaA := ave(3, "09:32", "05095", "02140")
	viaA.Fares = []fare{{Code: "N1010", Name: "Básico", PriceEUR: 28.1}}
	viaB := ave(4, "09:32", "05095", "02156")
	viaB.Fares = []fare{{Code: "N1010", Name: "Básico", PriceEUR: 30}}
	full := ave(5, "12:27", "03123")
	full.SoldOut = true
	j := journey{Trains: []train{direct, viaA, viaB, full}}

	for _, tc := range []struct {
		train, fare string
		maxFare     float64
		wantID      int
		wantFare    string
		wantErr     string
	}{
		{train: "6:27", fare: "basico", wantID: 2, wantFare: "N1010"},
		{train: "3063", fare: "ELIGE CONFORT", wantID: 2, wantFare: "N3010"},
		{train: "03063", fare: "n2010", wantID: 2, wantFare: "N2010"},
		{train: "06:27", fare: "cheapest", wantID: 2, wantFare: "N1010"}, // never the wheelchair-only fare
		{train: "05095+02156", fare: "basico", wantID: 4, wantFare: "N1010"},
		{train: "02140", fare: "basico", wantID: 3, wantFare: "N1010"},
		{train: "09:32", fare: "basico", wantErr: "matches several trains"},
		{train: "05095", fare: "basico", wantErr: "matches several trains"},
		{train: "12:27", fare: "basico", wantErr: "sold out"},
		{train: "07:00", fare: "basico", wantErr: "no train matches"},
		{train: "06:27", fare: "turista", wantErr: "available: Básico (N1010) 49.80"},
		{train: "06:27", fare: "plaza h", wantErr: "only for wheelchair spaces"},
		{train: "06:27", fare: "elige", maxFare: 50, wantErr: "above --max-fare"},
	} {
		got, err := choose(j, tc.train, tc.fare, tc.maxFare)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s/%s: got %v, want error containing %q", tc.train, tc.fare, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.Train.ID != tc.wantID || got.Fare.Code != tc.wantFare {
			t.Errorf("%s/%s: got train %d fare %s err %v, want %d %s", tc.train, tc.fare, got.Train.ID, got.Fare.Code, err, tc.wantID, tc.wantFare)
		}
	}
}
