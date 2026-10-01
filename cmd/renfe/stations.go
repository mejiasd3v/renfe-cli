package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const stationsURL = "https://www.renfe.com/content/dam/renfe/es/General/buscadores/javascript/estacionesEstaticas.js"

type station struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Key      string `json:"key"`      // the search form's cdgoOrigen/cdgoDestino value
	Priority int    `json:"priority"` // lower is more prominent on renfe.com
}

type stationCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Stations  []station `json:"stations"`
}

// loadStations returns Renfe's station list, refreshing the local copy weekly.
// A stale copy is used when renfe.com cannot be reached.
func loadStations(dir string, client *http.Client) ([]station, error) {
	path := filepath.Join(dir, "stations.json")
	var cache stationCache
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &cache) == nil && time.Since(cache.FetchedAt) < 7*24*time.Hour && len(cache.Stations) > 0 {
		return cache.Stations, nil
	}
	fresh, err := fetchStations(client)
	if err != nil {
		if len(cache.Stations) > 0 {
			return cache.Stations, nil
		}
		return nil, err
	}
	data, err := json.Marshal(stationCache{FetchedAt: time.Now(), Stations: fresh})
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return nil, fmt.Errorf("cannot cache stations: %w", err)
	}
	return fresh, nil
}

func fetchStations(client *http.Client) ([]station, error) {
	req, err := http.NewRequest(http.MethodGet, stationsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot download Renfe stations: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot download Renfe stations (HTTP %d)", resp.StatusCode)
	}
	return parseStations(body)
}

// parseStations reads estacionesEstaticas.js, which renfe.com serves as Latin-1.
func parseStations(body []byte) ([]station, error) {
	text := string(body)
	if !utf8.Valid(body) {
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		text = string(runes)
	}
	type raw struct {
		Code     string `json:"cdgoEstacion"`
		Name     string `json:"desgEstacion"`
		Key      string `json:"clave"`
		Priority int    `json:"nmroPrioridad"`
	}
	byCode := map[string]station{}
	for _, variable := range []string{"estacionesEstatico", "estacionesDestacada"} {
		start := strings.Index(text, "var "+variable+"=")
		if start < 0 {
			if variable == "estacionesEstatico" {
				return nil, errors.New("unexpected Renfe station list format")
			}
			continue
		}
		var list []raw
		if err := json.NewDecoder(strings.NewReader(text[start+len("var "+variable+"="):])).Decode(&list); err != nil {
			return nil, fmt.Errorf("unexpected Renfe station list format: %w", err)
		}
		for _, r := range list {
			if r.Code == "" || r.Key == "" || r.Name == "" {
				continue
			}
			if old, ok := byCode[r.Code]; !ok || r.Priority < old.Priority {
				byCode[r.Code] = station(r)
			}
		}
	}
	if len(byCode) == 0 {
		return nil, errors.New("Renfe station list is empty")
	}
	stations := make([]station, 0, len(byCode))
	for _, s := range byCode {
		stations = append(stations, s)
	}
	sortStations(stations)
	return stations, nil
}

func sortStations(stations []station) {
	slices.SortFunc(stations, func(a, b station) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(a.Name, b.Name)
	})
}

var accentFolder = strings.NewReplacer(
	"Á", "A", "À", "A", "Ä", "A", "Â", "A", "É", "E", "È", "E", "Ë", "E", "Ê", "E",
	"Í", "I", "Ì", "I", "Ï", "I", "Î", "I", "Ó", "O", "Ò", "O", "Ö", "O", "Ô", "O",
	"Ú", "U", "Ù", "U", "Ü", "U", "Û", "U", "Ñ", "N", "Ç", "C",
)

// words folds case and accents so "Cádiz" matches "CADIZ".
func words(s string) []string {
	s = accentFolder.Replace(strings.ToUpper(s))
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
}

// matchStations returns stations whose name words start with every query word,
// most prominent first. An exact code or full-name match comes first on its own.
func matchStations(stations []station, query string) []station {
	q := words(query)
	if len(q) == 0 {
		return nil
	}
	var matches []station
	for _, s := range stations {
		if strings.EqualFold(s.Code, strings.TrimSpace(query)) || slices.Equal(words(s.Name), q) {
			return []station{s}
		}
		name := words(s.Name)
		if !slices.ContainsFunc(q, func(qw string) bool {
			return !slices.ContainsFunc(name, func(nw string) bool { return strings.HasPrefix(nw, qw) })
		}) {
			matches = append(matches, s)
		}
	}
	return matches
}

// resolveStation picks the most prominent match, as renfe.com's autocomplete lists it first.
func resolveStation(stations []station, query string, stderr io.Writer) (station, error) {
	matches := matchStations(stations, query)
	if len(matches) == 0 {
		return station{}, fmt.Errorf("no Renfe station matches %q; try renfe stations %q", query, query)
	}
	if len(matches) > 1 {
		fmt.Fprintf(stderr, "%q -> %s [%s] (%d other matches; see renfe stations %q)\n", query, matches[0].Name, matches[0].Code, len(matches)-1, query)
	}
	return matches[0], nil
}
