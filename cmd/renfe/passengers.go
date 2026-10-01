package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// passenger is one traveller from the passengers file. Values go to Renfe's
// passenger form unchanged; Renfe validates them.
type passenger struct {
	ID           string `json:"id"`
	Type         string `json:"type"` // adult, child (4-13) or infant (under 4)
	Name         string `json:"name"`
	Surname1     string `json:"surname1"`
	Surname2     string `json:"surname2"`
	DocumentType string `json:"document_type"` // DNI, NIE or passport
	Document     string `json:"document"`
	Email        string `json:"email"`
	Phone        string `json:"phone"` // international format, e.g. +34600000000
}

type passengerFile struct {
	BizumPhone string      `json:"bizum_phone"`
	Passengers []passenger `json:"passengers"`
}

// documentCodes are the values of the passenger form's "tipoDocumento" select.
var documentCodes = map[string]string{"dni": "0021", "passport": "0022", "nie": "0023"}

var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func loadPassengerFile(path string, stderr io.Writer) (passengerFile, error) {
	var f passengerFile
	info, err := os.Stat(path)
	if err != nil {
		return f, fmt.Errorf("cannot read passengers file: %w (see passengers.example.json in ~/.config/renfe)", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(stderr, "Warning: %s is readable by other users; run chmod 600 on it\n", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("invalid passengers file %s: %w", path, err)
	}
	return f, nil
}

// pickPassengers returns the requested travellers in order, validated for Renfe's form.
// The first one is the buyer and needs an email and phone.
func pickPassengers(f passengerFile, ids []string) ([]passenger, error) {
	byID := map[string]passenger{}
	for _, p := range f.Passengers {
		byID[p.ID] = p
	}
	var out []passenger
	seen := map[string]bool{}
	for i, id := range ids {
		p, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("no passenger with id %q in the passengers file", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("passenger %q is listed twice", id)
		}
		seen[id] = true
		if i == 0 && p.Type != "adult" {
			return nil, errors.New("the first passenger (the buyer) must be an adult")
		}
		if err := p.validate(i == 0); err != nil {
			return nil, fmt.Errorf("passenger %q: %w", id, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("no passengers selected")
	}
	return out, nil
}

func (p passenger) validate(buyer bool) error {
	switch {
	case p.Type != "adult" && p.Type != "child" && p.Type != "infant":
		return errors.New(`type must be "adult", "child" or "infant"`)
	case strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Surname1) == "":
		return errors.New("name and surname1 are required")
	case documentCodes[strings.ToLower(p.DocumentType)] == "" && p.Type != "infant":
		return errors.New(`document_type must be "DNI", "NIE" or "passport"`)
	case strings.TrimSpace(p.Document) == "" && p.Type != "infant":
		return errors.New("document is required")
	case buyer && !strings.Contains(p.Email, "@"):
		return errors.New("the buyer needs an email")
	case (buyer || p.Phone != "") && !phonePattern.MatchString(p.Phone):
		return errors.New("phone must be in international format, e.g. +34600000000")
	}
	return nil
}

// countParty counts travellers by type for the search form.
func countParty(ps []passenger) party {
	var p party
	for _, t := range ps {
		switch t.Type {
		case "adult":
			p.Adults++
		case "child":
			p.Children++
		case "infant":
			p.Infants++
		}
	}
	return p
}
