# renfe-cli

An unofficial command-line client for [Renfe](https://www.renfe.com), Spain's national railway, with an agent skill for Claude Code and Codex. It searches trains and fares, books them, pays by Bizum and saves the ticket PDF. Every payment still needs the buyer's approval in their bank app.

```sh
$ renfe search --from madrid --to barcelona --date 2026-10-15 --format table
OUTBOUND 2026-10-15  MADRID-PUERTA DE ATOCHA-ALMUDENA GRANDES -> BARCELONA-SANTS
DEPART  ARRIVE  TIME  TRAINS     FARES (EUR per passenger)
06:16   09:24   3h08  AVE 03301  Básico 49.80 | Elige 54.80 | Elige Confort 86.10 | Prémium 105.10
06:27   09:50   3h23  AVE 03063  Básico 49.80 | Elige 54.80 | Elige Confort 59.60 | Prémium 78.60
...
```

This project is not affiliated with or endorsed by Renfe. It drives renfe.com's website the way a browser does, and the site can change without notice. Use it within Renfe's terms. You are responsible for every purchase it makes.

## Install

Prebuilt binaries for macOS and Linux (Intel and ARM) are on the [releases page](https://github.com/mejiasd3v/renfe-cli/releases). For example, on an Apple silicon Mac:

```sh
curl -fsSLO https://github.com/mejiasd3v/renfe-cli/releases/download/v0.1.0/renfe_0.1.0_darwin_arm64.tar.gz
tar -xzf renfe_0.1.0_darwin_arm64.tar.gz renfe && mv renfe ~/.local/bin/
```

Each release has a `checksums.txt` and GitHub build-provenance attestations (`gh attestation verify renfe_0.1.0_darwin_arm64.tar.gz -R mejiasd3v/renfe-cli`). The binaries are not notarized by Apple: one downloaded with a browser needs `xattr -d com.apple.quarantine renfe` before macOS runs it.

With Go 1.27 or later you can instead run `go install github.com/mejiasd3v/renfe-cli/cmd/renfe@latest`.

Searching needs only network access. Booking also needs a Chromium-based browser: Helium, Google Chrome or Chromium, or any other set in `RENFE_BROWSER`.

## Agent skill

The skill in [`skills/renfe`](skills/renfe/SKILL.md) teaches an agent the search, book and pay workflow and its safety rules. It works with Claude Code, Codex, OpenClaw and Hermes Agent. The agent runs the CLI through the skill's `scripts/renfe` launcher, which picks a binary in this order:

1. The binary bundled in the skill's `bin/`.
2. A `renfe` on PATH of the same version.
3. The matching release binary, downloaded once into `~/.cache/renfe-cli` and checked against the release's SHA-256 checksums.

So any install method below works without Go.

**One command for every agent on your machine:**

```sh
renfe skill install                       # the agents found in your home folder
renfe skill install --agent claude,hermes # or choose: claude, codex, openclaw, hermes, all
```

This writes the skill, with a copy of the running `renfe` binary, into each agent's skills folder:

| Agent | Skills folder |
| --- | --- |
| Claude Code | `~/.claude/skills/renfe` (`CLAUDE_CONFIG_DIR` respected) |
| Codex | `~/.agents/skills/renfe` |
| OpenClaw | `~/.openclaw/skills/renfe` (`OPENCLAW_STATE_DIR` respected); with the default state folder OpenClaw also loads the Codex copy in `~/.agents/skills`, so no second copy is made |
| Hermes Agent | `~/.hermes/skills/renfe` (`HERMES_HOME` respected); skipped when Hermes already loads `~/.agents/skills` through `skills.external_dirs`, because two copies make Hermes reject the skill name as ambiguous |

The command never writes through a symlink or replaces a folder that is not this skill.

**Native installers:**

```sh
# Claude Code
claude plugin marketplace add mejiasd3v/renfe-cli && claude plugin install renfe-cli@renfe-cli

# Codex
codex plugin marketplace add mejiasd3v/renfe-cli && codex plugin add renfe-cli@renfe-cli

# Hermes Agent
hermes skills install mejiasd3v/renfe-cli/skills/renfe
```

Every release also has skill bundles with the binary inside, `renfe-skill_<version>_<os>_<arch>.zip`. Unzip one into any agent's skills folder. OpenClaw's own `skills install` needs `SKILL.md` at the repository root, so for OpenClaw use `renfe skill install`, a skill bundle, or the shared `~/.agents/skills` folder.

The skill's frontmatter carries metadata for each agent:

- **OpenClaw:** emoji, supported OS, and a `go install` option in its skills UI.
- **Hermes:** tags.
- **Codex:** display text in `agents/openai.yaml`.

Bookings still need a passengers file (below).

## Find trains

```sh
renfe stations "barcelona sants" --format table
renfe search --from madrid --to barcelona --date 2026-10-15
renfe search --from valencia --to sevilla --date 2026-10-20 --return 2026-10-22 --adults 2 --children 1
renfe search --from atocha --to "barcelona sants" --date 2026-10-15 --direct
```

- `--from` and `--to` take a station name or code. Matching ignores case and accents, and each word may be a prefix (`"madrid atocha"`). When several stations match, the CLI picks the one renfe.com lists first and says so on stderr. Entries such as `MADRID (TODAS)` cover every station in that city.
- `--date` and `--return` use `YYYY-MM-DD`. Passengers: `--adults` (14+, default 1), `--children` (4 to 13), `--infants` (under 4), 9 in total at most.
- JSON (the default) lists, per journey, every train with its legs, `sold_out`, `min_price_eur`, its fares (`code`, `name`, `price_eur`, `class`, `conditions`) and `notices`, plus Renfe's ±3-day `price_calendar`. A change of station between legs appears as a notice.
- Fare prices are per passenger, as Renfe's results page lists them. The total you pay is the one `book` reports from the payment page.

## Passengers file

Bookings read travellers from `~/.config/renfe/passengers.json`. Start from [`passengers.example.json`](passengers.example.json) and keep the file private (`chmod 600`; the CLI warns otherwise).

```json
{
  "bizum_phone": "+34600000000",
  "passengers": [
    {"id": "me", "type": "adult", "name": "Nombre", "surname1": "Apellido", "surname2": "Apellido",
     "document_type": "DNI", "document": "12345678Z", "email": "you@example.com", "phone": "+34600000000"}
  ]
}
```

- `type` is `adult`, `child` (4 to 13) or `infant` (under 4). `document_type` is `DNI`, `NIE` or `passport`.
- The first `--passenger` is the buyer: an adult with `email` and `phone`. Renfe sends the tickets to that email.
- `bizum_phone` is the Spanish number registered with Bizum; it defaults to the buyer's phone.
- Values go to Renfe's form unchanged and Renfe validates them. Unknown keys are rejected.

## Book and pay

```sh
# 1. Check the choice. Nothing is selected on renfe.com and no browser opens.
renfe book --from madrid --to guadalajara --date 2026-10-08 --train 07:46 --fare cheapest --passenger me --dry-run

# 2. Fill everything up to the payment page and get Renfe's total (status ready_to_pay).
renfe book --from madrid --to guadalajara --date 2026-10-08 --train 07:46 --fare cheapest --passenger me --max-total 10

# 3. Pay with Bizum; the buyer approves the request in their bank app.
renfe pay --max-total 6.30

# Steps 2 and 3 in one command:
renfe book ... --passenger me --pay bizum --max-total 10
```

`book` searches again and selects the train and fare as renfe.com's "Seleccionar" button does. It checks that Renfe's passenger page shows the selected train numbers, then opens that page in the CLI's own browser. With `--passenger` it fills in "Datos Viajeros" and skips the optional extras. On "Método de pago" it fills the buyer's email and phone, selects Bizum and accepts Renfe's purchase conditions, then stops and reports Renfe's total.

`pay` (or `book --pay bizum`) checks Renfe's total against `--max-total` and presses "Finaliza tu compra". It then checks the amount on the Redsys Bizum page, enters `bizum_phone`, and waits up to `--wait` (default 5 minutes) for Renfe's confirmation page. Nothing is charged until the buyer approves the request in their bank app. On confirmation it saves the ticket PDF (see [Tickets](#tickets)).

- `--train` takes the departure time (`06:27`, `6:27`) or train numbers (`3063`, `03063`, `05095+02140` for a connection). An ambiguous value is an error listing the options. Sold-out trains are refused.
- `--fare` takes a fare name (`basico`, `elige confort`, `premium`), a code (`N1010`, AVLO's `LC107`) or `cheapest`. Fares reserved for wheelchair spaces are refused; book those on renfe.com.
- `--max-fare` caps the per-passenger fare when choosing. `--max-total` caps Renfe's total for the whole purchase: `book` and `pay` stop before paying if Renfe's or Redsys's amount is higher. `pay` and `--pay` require it.
- Without `--passenger`, `book` stops on the passenger page so you can finish by hand, with any payment method.
- Renfe expires idle sessions after some minutes; run `pay` soon after `book`.

| status | meaning |
| --- | --- |
| `dry_run` | Train and fare resolved; nothing sent to Renfe beyond the search. |
| `awaiting_passenger_details` | Browser open on the passenger page for manual completion. |
| `ready_to_pay` | Payment page open with Bizum selected; `total_eur` is Renfe's total. |
| `confirmed` | Renfe's confirmation page appeared; `locator` is the booking reference and `ticket_pdf` the saved ticket. |
| `payment_pending` | No confirmation within `--wait`. If the request was approved, check the buyer's email and the browser before booking again. |

The JSON also includes the selected `outbound`/`return` train and fare, `passengers`, `passenger_ids`, `payment_method`, `login_required` (true when Renfe asks to sign in before buying that train) and `next_step`. Errors exit 1 with Renfe's own message where it gives one, such as a rejected document number.

## Tickets

```sh
renfe ticket ABC123                 # download again to ~/.config/renfe/tickets/ABC123.pdf
renfe ticket                        # the purchase whose confirmation page is open in the CLI's browser
renfe ticket ABC123 --tickets-dir ~/Downloads
```

After a confirmed payment, `pay` and `book --pay` download the ticket behind Renfe's "Descárgalos en PDF" link to `--tickets-dir`, by default `~/.config/renfe/tickets/<LOCATOR>.pdf`. They report its path as `ticket_pdf`. If the download fails, the purchase still reports `confirmed`, with the reason in `ticket_error`. Never pay again because of a ticket error.

Each confirmed purchase is recorded in `~/.config/renfe/bookings.json` (locator, time, total, ticket link, PDF path), which `renfe ticket LOCATOR` uses to download again. Tickets bought elsewhere cannot be fetched by locator; use Renfe's email. Renfe's ticket link needs no login, so the records file and PDFs are owner-only (600, folder 700). When Renfe has not generated a PDF, its link ends in `NO_PDF` and the CLI says so; the ticket is still emailed.

## Safety

- Nothing is charged without the buyer approving the Bizum request in their bank app.
- `--max-total` is checked against Renfe's payment page and again against the amount Redsys shows, before the Bizum request is sent.
- A lock file makes concurrent `book`/`pay` runs fail instead of sharing one browser and purchase.
- Starting a new booking closes earlier Renfe and Redsys tabs in the CLI's browser, because the new session replaces theirs.
- Passenger details travel only from the passengers file to Renfe's form, as JSON data; they never appear in command arguments or output.
- The agent skill tells agents to buy only when asked, to state the train and total before requesting payment, and never to pay twice for one trip.

## How it works

The sale site, venta.renfe.com, is a Java application driven by DWR (Direct Web Remoting) calls. Searching uses direct HTTP: the CLI submits renfe.com's search form, opens a DWR script session and calls `trainEnlacesManager.getTrainsList`. Selecting a train calls `showModalLogin`, `validarTarifasSeleccionadas` and `setTrenEnlaceSeleccionado`.

The purchase then moves to the CLI's own browser, started with a dedicated profile in `~/.config/renfe/browser-<name>/` and a DevTools endpoint on `127.0.0.1:19229`. The CLI copies its session cookies into that browser and opens Renfe's pages there: `datosViajeEnlaces.do`, `personalizaVentaEnlaces.do`, `formasDePagoEnlaces.do`, Redsys's Bizum page and `finalCompraEnlaceVXY.do`. It fills them through `Runtime.evaluate`, passing values as JSON data and firing the same events typing would, so Renfe's own validation runs. Your everyday browser profile is never read or modified. While the browser is open, local processes can reach its DevTools port, so quit it when you are done.

The station list comes from renfe.com's `estacionesEstaticas.js` and is cached for a week in `~/.config/renfe/stations.json`. Errors are explicit: Renfe's queue-it waiting room, dates sold only on Renfe's upcoming new website (announced on the results page), and DWR exceptions such as an expired session.

## Tested

On 2026-10-01:

- **CLI:** station lookup; one-way, round-trip and connecting searches; `book` without passengers for one-way and round trips.
- **Live purchase:** one end-to-end purchase of a regional ticket. It ran `book --passenger`, then `pay`, and the Bizum request was approved in the bank app. It returned `confirmed` with Renfe's locator, and `renfe ticket` saved the PDF.
- **Skill in Claude Code and Codex:** loaded from their plugin marketplaces, installed from GitHub.
- **Skill in OpenClaw:** OpenClaw 2026.9.4 lists it as eligible and visible to the model.
- **Skill in Hermes:** its frontmatter parses with Hermes Agent's own parser. The Hermes install command was not run.

Not exercised yet:

- Several passengers, children or infants on the passenger form.
- An AVE or AVLO purchase.
- A declined or expired Bizum request.
- Trains that require signing in.
- The automatic PDF download inside a live `pay`.

## Limits

- Bizum only. Card, Google Pay, PayPal and Renfe points stay available by hand in the browser (`book` without `--passenger`).
- No cancellation, change or "Mis viajes" access.
- No seat selection, pets, bikes, discount cards, promo codes or assistance (Atendo) options.
- Cercanías/Rodalies tickets are not sold through this search.

## Development

```sh
make test     # unit tests, no network
make check    # tests, go vet and staticcheck
make build    # ./renfe
make dist     # release archives and skill bundles into dist/
```

To release, set the new version in `skills/renfe/scripts/renfe` (`VERSION`), `skills/renfe/SKILL.md` (`metadata.version` and the `go install` module) and both `plugin.json` files, then push a `vX.Y.Z` tag. `scripts/dist.sh` refuses to build if any of them disagree, and the release workflow publishes the archives, checksums and attestations.

The code lives in `cmd/renfe`: `session.go` and `dwr.go` for the HTTP/DWR protocol, `trains.go` for parsing, `choose.go` for train and fare selection, `browser.go` and `checkout.go` for the browser-driven purchase, `ticket.go` for PDFs. Changes to the purchase flow should be verified against renfe.com, since unit tests cannot cover the live pages.

## License

[MIT](LICENSE)
