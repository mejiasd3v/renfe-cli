package main

import (
	"reflect"
	"testing"
	"time"
)

// Trimmed from a live getTrainsList response for a Valencia-Sevilla round trip.
const trainListReply = `throw 'allowScriptTagRemoting is false.';
(function(){
var r=window.dwr._[0];
//#DWR-INSERT
//#DWR-REPLY
r.handleCallback("1","0",{actEtiqOferta:"SI",activoCuposAbonosRec:null,listadoTrenes:[{viajeIda:true,fechaOrigen:"20/10/2026",descripcionEstacionOrigen:"VALÈNCIA-JOAQUÍN SOROLLA",descripcionEstacionDestino:"SEVILLA-SANTA JUSTA",hayError:false,cdgoError:null,mensajeListaTrenVacia:null,resultPriceCalendar:{journeysPriceCalendar:[{date:"2026-10-20",minPrice:28,minPriceAvailable:true},{date:"2026-10-21",minPrice:0,minPriceAvailable:false}]},listviajeViewEnlaceBean:[{id:3,fecha:"2026-10-20",horaSalida:"09:32",horaLlegada:"16:39",duracionViajeTotalEnMinutos:427,directo:false,completo:false,tarifaMinima:"28,1",msgTrenNoDispo:null,trayectos:[{cdgoTren:"05095",codOperador:"LC",tipoTren:"AVLO",descripcionEstacionOrigen:"VALÈNCIA-JOAQUÍN SOROLLA",descripcionEstacionDestino:"MADRID-CHAMARTÍN-CLARA CAMPOAMOR",fecha:"2026-10-20",horaSalida:"09:32:00",horaLlegada:"11:20:00",lstIncidencias:[]},{cdgoTren:"02140",codOperador:"AV",tipoTren:"AVE",descripcionEstacionOrigen:"MADRID-PUERTA DE ATOCHA-ALMUDENA GRANDES",descripcionEstacionDestino:"SEVILLA-SANTA JUSTA",fecha:"2026-10-20",horaSalida:"13:42:00",horaLlegada:"16:39:00",lstIncidencias:[{cdgoInc:"PETG",description:"El coche 7 es <b>Petfriendly</b>.<br>",icon:"incid.gif  "}]}],tarifasDisponibles:[{codigoTarifa:"N1010",titulo:"Básico",precioTarifa:"28,1",cdgoClase:"T",soloPlazasH:false,prestaciones:["Permite 1 cambio hasta 24 horas antes (10 &#128; + diferencia de precio)",null],tpEnlaceSilencio:"4#05095-3#02140",oferta:{color:"",tipo:"N",txtBoton:""}}]}]},{viajeIda:false,fechaOrigen:"22/10/2026",descripcionEstacionOrigen:"SEVILLA-SANTA JUSTA",descripcionEstacionDestino:"VALÈNCIA-JOAQUÍN SOROLLA",hayError:"true",cdgoError:null,mensajeListaTrenVacia:null,resultPriceCalendar:null,listviajeViewEnlaceBean:[]}]});
})();
`

func TestTrainListDecoding(t *testing.T) {
	raw, err := dwrReply([]byte(trainListReply))
	if err != nil {
		t.Fatal(err)
	}
	q := query{From: station{Name: "VALENCIA (TODAS)"}, To: station{Name: "SEVILLA-SANTA JUSTA"}, Date: time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local), Return: time.Date(2026, 10, 22, 0, 0, 0, 0, time.Local)}
	outbound, ret, err := parseTrainList(raw, q)
	if err != nil {
		t.Fatal(err)
	}
	price := 28.1
	want := train{
		ID: 3, Date: "2026-10-20", Departure: "09:32", Arrival: "16:39", DurationMinutes: 427, MinPriceEUR: &price,
		Legs: []leg{
			{Train: "05095", Type: "AVLO", Operator: "LC", From: "VALÈNCIA-JOAQUÍN SOROLLA", To: "MADRID-CHAMARTÍN-CLARA CAMPOAMOR", Date: "2026-10-20", Departure: "09:32", Arrival: "11:20"},
			{Train: "02140", Type: "AVE", Operator: "AV", From: "MADRID-PUERTA DE ATOCHA-ALMUDENA GRANDES", To: "SEVILLA-SANTA JUSTA", Date: "2026-10-20", Departure: "13:42", Arrival: "16:39"},
		},
		Fares: []fare{{Code: "N1010", Name: "Básico", PriceEUR: 28.1, Class: "T", Conditions: []string{"Permite 1 cambio hasta 24 horas antes (10 € + diferencia de precio)"}, seatLink: "4#05095-3#02140"}},
		Notices: []string{
			"El coche 7 es Petfriendly.",
			"Change of station: leg 1 arrives at MADRID-CHAMARTÍN-CLARA CAMPOAMOR, leg 2 departs from MADRID-PUERTA DE ATOCHA-ALMUDENA GRANDES",
		},
	}
	if len(outbound.Trains) != 1 || !reflect.DeepEqual(outbound.Trains[0], want) {
		t.Fatalf("outbound trains:\n got %+v\nwant %+v", outbound.Trains, want)
	}
	if outbound.From != "VALÈNCIA-JOAQUÍN SOROLLA" || !reflect.DeepEqual(outbound.PriceCalendar, []dayPrice{{"2026-10-20", 28}}) {
		t.Fatalf("outbound journey: %+v", outbound)
	}
	if ret == nil || len(ret.Trains) != 0 || ret.Message != "Renfe reported an error for this journey" {
		t.Fatalf("return journey: %+v", ret)
	}
}
