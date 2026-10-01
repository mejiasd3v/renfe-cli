package main

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// query is one train search as renfe.com's search box submits it.
type query struct {
	From, To                  station
	Date, Return              time.Time // Return is zero for one-way trips
	Adults, Children, Infants int
}

// party counts travellers by type, as Renfe's search form takes them.
type party struct {
	Adults   int `json:"adults"`
	Children int `json:"children"`
	Infants  int `json:"infants"`
}

type leg struct {
	Train     string `json:"train"`
	Type      string `json:"type"`
	Operator  string `json:"operator"`
	From      string `json:"from"`
	To        string `json:"to"`
	Date      string `json:"date"`
	Departure string `json:"departure"`
	Arrival   string `json:"arrival"`
}

type fare struct {
	Code           string   `json:"code"`
	Name           string   `json:"name"`
	PriceEUR       float64  `json:"price_eur"`
	Class          string   `json:"class"`
	WheelchairOnly bool     `json:"wheelchair_only,omitempty"`
	Conditions     []string `json:"conditions,omitempty"`
	seatLink       string   // tpEnlaceSilencio, echoed back when the fare is selected
}

type train struct {
	ID              int      `json:"id"`
	Date            string   `json:"date"`
	Departure       string   `json:"departure"`
	Arrival         string   `json:"arrival"`
	DurationMinutes int      `json:"duration_minutes"`
	Direct          bool     `json:"direct"`
	SoldOut         bool     `json:"sold_out"`
	MinPriceEUR     *float64 `json:"min_price_eur"`
	Legs            []leg    `json:"legs"`
	Fares           []fare   `json:"fares,omitempty"`
	Notices         []string `json:"notices,omitempty"`
}

type dayPrice struct {
	Date        string  `json:"date"`
	MinPriceEUR float64 `json:"min_price_eur"`
}

type journey struct {
	Date          string     `json:"date"`
	From          string     `json:"from"`
	To            string     `json:"to"`
	Trains        []train    `json:"trains"`
	Message       string     `json:"message,omitempty"`
	PriceCalendar []dayPrice `json:"price_calendar,omitempty"`
}

type searchResult struct {
	Origin      station  `json:"origin"`
	Destination station  `json:"destination"`
	Passengers  party    `json:"passengers"`
	Outbound    journey  `json:"outbound"`
	Return      *journey `json:"return,omitempty"`
}

type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	*b = flexBool(string(data) == "true" || string(data) == `"true"`)
	return nil
}

type rawTrainList struct {
	ListadoTrenes []struct {
		ViajeIda              bool     `json:"viajeIda"`
		FechaOrigen           string   `json:"fechaOrigen"`
		From                  string   `json:"descripcionEstacionOrigen"`
		To                    string   `json:"descripcionEstacionDestino"`
		HayError              flexBool `json:"hayError"`
		CdgoError             string   `json:"cdgoError"`
		MensajeListaTrenVacia string   `json:"mensajeListaTrenVacia"`
		ResultPriceCalendar   *struct {
			Days []struct {
				Date      string  `json:"date"`
				MinPrice  float64 `json:"minPrice"`
				Available bool    `json:"minPriceAvailable"`
			} `json:"journeysPriceCalendar"`
		} `json:"resultPriceCalendar"`
		Trains []struct {
			ID              int     `json:"id"`
			Fecha           string  `json:"fecha"`
			HoraSalida      string  `json:"horaSalida"`
			HoraLlegada     string  `json:"horaLlegada"`
			DurationMinutes int     `json:"duracionViajeTotalEnMinutos"`
			Directo         bool    `json:"directo"`
			Completo        bool    `json:"completo"`
			TarifaMinima    string  `json:"tarifaMinima"`
			MsgTrenNoDispo  *string `json:"msgTrenNoDispo"`
			Trayectos       []struct {
				CdgoTren    string `json:"cdgoTren"`
				CodOperador string `json:"codOperador"`
				TipoTren    string `json:"tipoTren"`
				From        string `json:"descripcionEstacionOrigen"`
				To          string `json:"descripcionEstacionDestino"`
				Fecha       string `json:"fecha"`
				HoraSalida  string `json:"horaSalida"`
				HoraLlegada string `json:"horaLlegada"`
				Incidencias []struct {
					Description string `json:"description"`
				} `json:"lstIncidencias"`
			} `json:"trayectos"`
			Tarifas []struct {
				Codigo       string   `json:"codigoTarifa"`
				Titulo       string   `json:"titulo"`
				Precio       string   `json:"precioTarifa"`
				Clase        string   `json:"cdgoClase"`
				SoloPlazasH  bool     `json:"soloPlazasH"`
				Prestaciones []string `json:"prestaciones"`
				Silencio     string   `json:"tpEnlaceSilencio"`
			} `json:"tarifasDisponibles"`
		} `json:"listviajeViewEnlaceBean"`
	} `json:"listadoTrenes"`
}

func parseTrainList(raw json.RawMessage, q query) (journey, *journey, error) {
	var data rawTrainList
	if err := json.Unmarshal(raw, &data); err != nil {
		return journey{}, nil, fmt.Errorf("unexpected train list format: %w", err)
	}
	outbound := journey{Date: q.Date.Format(time.DateOnly), From: q.From.Name, To: q.To.Name, Trains: []train{}}
	var ret *journey
	if !q.Return.IsZero() {
		ret = &journey{Date: q.Return.Format(time.DateOnly), From: q.To.Name, To: q.From.Name, Trains: []train{}}
	}
	for _, list := range data.ListadoTrenes {
		j := &outbound
		if !list.ViajeIda {
			if ret == nil {
				continue
			}
			j = ret
		}
		if list.From != "" {
			j.From, j.To = list.From, list.To
		}
		switch {
		case list.MensajeListaTrenVacia != "":
			j.Message = cleanText(list.MensajeListaTrenVacia)
		case list.CdgoError == "CRC1":
			j.Message = "Renfe sells this route as Cercanías/Rodalies, not through this search"
		case bool(list.HayError):
			j.Message = "Renfe reported an error for this journey"
		}
		if list.ResultPriceCalendar != nil {
			for _, d := range list.ResultPriceCalendar.Days {
				if d.Available {
					j.PriceCalendar = append(j.PriceCalendar, dayPrice{d.Date, d.MinPrice})
				}
			}
		}
		for _, t := range list.Trains {
			out := train{ID: t.ID, Date: t.Fecha, Departure: t.HoraSalida, Arrival: t.HoraLlegada, DurationMinutes: t.DurationMinutes, Direct: t.Directo, SoldOut: t.Completo, Legs: []leg{}, Fares: []fare{}}
			if p, ok := parseEuro(t.TarifaMinima); ok {
				out.MinPriceEUR = &p
			}
			if t.MsgTrenNoDispo != nil && *t.MsgTrenNoDispo != "" {
				out.Notices = append(out.Notices, cleanText(*t.MsgTrenNoDispo))
			}
			for _, l := range t.Trayectos {
				out.Legs = append(out.Legs, leg{Train: l.CdgoTren, Type: strings.TrimSpace(l.TipoTren), Operator: l.CodOperador, From: l.From, To: l.To, Date: l.Fecha, Departure: hhmm(l.HoraSalida), Arrival: hhmm(l.HoraLlegada)})
				for _, inc := range l.Incidencias {
					if text := cleanText(inc.Description); text != "" {
						out.Notices = append(out.Notices, text)
					}
				}
			}
			for i := 1; i < len(out.Legs); i++ {
				if prev, next := out.Legs[i-1], out.Legs[i]; prev.To != next.From {
					out.Notices = append(out.Notices, fmt.Sprintf("Change of station: leg %d arrives at %s, leg %d departs from %s", i, prev.To, i+1, next.From))
				}
			}
			for _, f := range t.Tarifas {
				price, ok := parseEuro(f.Precio)
				if !ok {
					continue
				}
				var conditions []string
				for _, c := range f.Prestaciones {
					if text := cleanText(c); text != "" {
						conditions = append(conditions, text)
					}
				}
				out.Fares = append(out.Fares, fare{Code: f.Codigo, Name: f.Titulo, PriceEUR: price, Class: f.Clase, WheelchairOnly: f.SoloPlazasH, Conditions: conditions, seatLink: f.Silencio})
			}
			j.Trains = append(j.Trains, out)
		}
	}
	if len(outbound.Trains) == 0 && outbound.Message == "" {
		outbound.Message = "No trains found for this route and date"
	}
	if ret != nil && len(ret.Trains) == 0 && ret.Message == "" {
		ret.Message = "No trains found for this route and date"
	}
	return outbound, ret, nil
}

// parseEuro reads Renfe's "49,8" style prices.
func parseEuro(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && s != ""
}

func hhmm(s string) string {
	if len(s) == 8 && s[2] == ':' && s[5] == ':' {
		return s[:5]
	}
	return s
}

var (
	tagPattern   = regexp.MustCompile(`<[^>]*>`)
	blockPattern = regexp.MustCompile(`(?i)^</?(br|p|div|li|ul|ol|tr|td|h[1-6])\b`)
)

// cleanText turns Renfe's HTML snippets into plain text: block tags become spaces,
// inline tags such as <b> disappear.
func cleanText(s string) string {
	s = tagPattern.ReplaceAllStringFunc(s, func(tag string) string {
		if blockPattern.MatchString(tag) {
			return " "
		}
		return ""
	})
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}
