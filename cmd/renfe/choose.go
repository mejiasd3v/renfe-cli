package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// selection is one chosen train and fare for the outbound or return journey.
type selection struct {
	Train train `json:"train"`
	Fare  fare  `json:"fare"`
}

var clockPattern = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})$`)

// trainNumbers is the --train value that identifies t, e.g. "05871+02100".
func trainNumbers(t train) string {
	numbers := make([]string, len(t.Legs))
	for i, l := range t.Legs {
		numbers[i] = l.Train
	}
	return strings.Join(numbers, "+")
}

func trainLabel(t train) string { return t.Departure + " " + trainNumbers(t) }

// pickTrain finds one train by departure time or by its train number(s).
func pickTrain(trains []train, sel string) (train, error) {
	sel = strings.TrimSpace(sel)
	var matches []train
	if m := clockPattern.FindStringSubmatch(sel); m != nil {
		hour, _ := strconv.Atoi(m[1])
		want := fmt.Sprintf("%02d:%s", hour, m[2])
		for _, t := range trains {
			if t.Departure == want {
				matches = append(matches, t)
			}
		}
	} else {
		parts := strings.Split(sel, "+")
		for i, p := range parts {
			parts[i] = strings.TrimLeft(strings.TrimSpace(p), "0")
		}
		for _, t := range trains {
			if trainNumbersMatch(t, parts) {
				matches = append(matches, t)
			}
		}
	}
	labels := func(ts []train) string {
		var l []string
		for _, t := range ts {
			l = append(l, trainLabel(t))
		}
		return strings.Join(l, ", ")
	}
	switch len(matches) {
	case 0:
		return train{}, fmt.Errorf("no train matches %q; available: %s", sel, labels(trains))
	case 1:
		if matches[0].SoldOut {
			return train{}, fmt.Errorf("train %s is sold out", trainLabel(matches[0]))
		}
		return matches[0], nil
	}
	return train{}, fmt.Errorf("%q matches several trains (%s); use train numbers such as %s", sel, labels(matches), trainNumbers(matches[0]))
}

func trainNumbersMatch(t train, parts []string) bool {
	number := func(l leg) string { return strings.TrimLeft(l.Train, "0") }
	if len(parts) == 1 {
		for _, l := range t.Legs {
			if parts[0] != "" && number(l) == parts[0] {
				return true
			}
		}
		return false
	}
	if len(parts) != len(t.Legs) {
		return false
	}
	for i, l := range t.Legs {
		if number(l) != parts[i] {
			return false
		}
	}
	return true
}

// pickFare finds a fare by code, by name ignoring case and accents, or the cheapest one.
func pickFare(t train, sel string) (fare, error) {
	var found *fare
	for i, f := range t.Fares {
		switch {
		case strings.EqualFold(sel, "cheapest"):
			if !f.WheelchairOnly && (found == nil || f.PriceEUR < found.PriceEUR) {
				found = &t.Fares[i]
			}
		case strings.EqualFold(f.Code, sel) || strings.Join(words(f.Name), " ") == strings.Join(words(sel), " "):
			found = &t.Fares[i]
		}
	}
	if found == nil {
		var names []string
		for _, f := range t.Fares {
			names = append(names, fmt.Sprintf("%s (%s) %.2f", f.Name, f.Code, f.PriceEUR))
		}
		if len(names) == 0 {
			return fare{}, fmt.Errorf("train %s has no fares for sale", trainLabel(t))
		}
		return fare{}, fmt.Errorf("no fare %q on train %s; available: %s", sel, trainLabel(t), strings.Join(names, ", "))
	}
	if found.WheelchairOnly {
		return fare{}, fmt.Errorf("fare %s on train %s is only for wheelchair spaces; book it on renfe.com", found.Name, trainLabel(t))
	}
	return *found, nil
}

func choose(j journey, trainSel, fareSel string, maxFare float64) (selection, error) {
	t, err := pickTrain(j.Trains, trainSel)
	if err != nil {
		return selection{}, err
	}
	f, err := pickFare(t, fareSel)
	if err != nil {
		return selection{}, err
	}
	if maxFare > 0 && f.PriceEUR > maxFare {
		return selection{}, fmt.Errorf("fare %s on %s costs %.2f EUR, above --max-fare %.2f", f.Name, trainLabel(t), f.PriceEUR, maxFare)
	}
	return selection{Train: t, Fare: f}, nil
}

func trainTypes(t train) string {
	var parts []string
	for _, l := range t.Legs {
		parts = append(parts, l.Type+" "+l.Train)
	}
	return strings.Join(parts, " + ")
}
