package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// tripFlags are the flags search and book share.
type tripFlags struct {
	from, to, date, ret, format string
	adults, children, infants   int
}

func (f *tripFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.from, "from", "", "origin station name or code")
	fs.StringVar(&f.to, "to", "", "destination station name or code")
	fs.StringVar(&f.date, "date", "", "outbound date, YYYY-MM-DD")
	fs.StringVar(&f.ret, "return", "", "return date, YYYY-MM-DD (round trip)")
	fs.IntVar(&f.adults, "adults", 1, "adult passengers (14 and over)")
	fs.IntVar(&f.children, "children", 0, "child passengers (4 to 13)")
	fs.IntVar(&f.infants, "infants", 0, "infant passengers (under 4)")
	fs.StringVar(&f.format, "format", "json", "json or table")
}

// countsSet reports whether --adults, --children or --infants was given explicitly.
func countsSet(fs *flag.FlagSet) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "adults" || f.Name == "children" || f.Name == "infants" })
	return set
}

// searchTrip validates the trip, resolves its stations and searches renfe.com.
// The session is kept for booking from the same results.
func searchTrip(f tripFlags, dir string, stderr io.Writer) (*session, *searchResult, error) {
	if f.from == "" || f.to == "" || f.date == "" {
		return nil, nil, errors.New("--from, --to and --date are required")
	}
	if f.adults < 1 || f.children < 0 || f.infants < 0 || f.adults+f.children+f.infants > 9 {
		return nil, nil, errors.New("passengers: at least 1 adult, no negative counts, 9 passengers at most")
	}
	q := query{Adults: f.adults, Children: f.children, Infants: f.infants}
	var err error
	if q.Date, err = parseDate(f.date, "--date"); err != nil {
		return nil, nil, err
	}
	if f.ret != "" {
		if q.Return, err = parseDate(f.ret, "--return"); err != nil {
			return nil, nil, err
		}
		if q.Return.Before(q.Date) {
			return nil, nil, errors.New("--return is before --date")
		}
	}
	stations, err := stationList(dir)
	if err != nil {
		return nil, nil, err
	}
	if q.From, err = resolveStation(stations, f.from, stderr); err != nil {
		return nil, nil, err
	}
	if q.To, err = resolveStation(stations, f.to, stderr); err != nil {
		return nil, nil, err
	}
	if q.From.Code == q.To.Code {
		return nil, nil, errors.New("origin and destination are the same station")
	}
	fmt.Fprintf(stderr, "Searching %s -> %s on %s...\n", q.From.Name, q.To.Name, q.Date.Format(time.DateOnly))
	s := newSession()
	result, err := s.search(q)
	return s, result, err
}

func runSearch(args []string, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var trip tripFlags
	trip.register(fs)
	direct := fs.Bool("direct", false, "only show trains without changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !validFormat(trip.format) {
		return errors.New("unexpected arguments or invalid --format; run renfe search --help")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	_, result, err := searchTrip(trip, dir, stderr)
	if err != nil {
		return err
	}
	if *direct {
		result.Outbound.Trains = directOnly(result.Outbound.Trains)
		if result.Return != nil {
			result.Return.Trains = directOnly(result.Return.Trains)
		}
	}
	if trip.format == "table" {
		return printSearchTable(out, result)
	}
	return writeJSON(out, result)
}

func parseDate(s, flagName string) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, s, time.Local)
	if err != nil {
		return d, fmt.Errorf("%s must be YYYY-MM-DD", flagName)
	}
	y, m, day := time.Now().Date()
	if d.Before(time.Date(y, m, day, 0, 0, 0, 0, time.Local)) {
		return d, fmt.Errorf("%s is in the past", flagName)
	}
	return d, nil
}

func directOnly(trains []train) []train {
	out := []train{}
	for _, t := range trains {
		if t.Direct {
			out = append(out, t)
		}
	}
	return out
}

func printSearchTable(out io.Writer, r *searchResult) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for i, j := range []*journey{&r.Outbound, r.Return} {
		if j == nil {
			continue
		}
		label := "OUTBOUND"
		if i == 1 {
			label = "RETURN"
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s %s  %s -> %s\n", label, j.Date, j.From, j.To)
		if j.Message != "" {
			fmt.Fprintln(w, j.Message)
		}
		if len(j.Trains) == 0 {
			continue
		}
		fmt.Fprintln(w, "DEPART\tARRIVE\tTIME\tTRAINS\tFARES (EUR per passenger)")
		for _, t := range j.Trains {
			var fares []string
			for _, f := range t.Fares {
				fares = append(fares, fmt.Sprintf("%s %.2f", f.Name, f.PriceEUR))
			}
			faresText := strings.Join(fares, " | ")
			if t.SoldOut {
				faresText = "SOLD OUT"
			}
			fmt.Fprintf(w, "%s\t%s\t%dh%02d\t%s\t%s\n", t.Departure, t.Arrival, t.DurationMinutes/60, t.DurationMinutes%60, trainTypes(t), faresText)
		}
	}
	return w.Flush()
}
