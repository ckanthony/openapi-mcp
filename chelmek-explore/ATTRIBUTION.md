# ATTRIBUTION

Chełmek EXPLORE — required attributions for data and content.

## OpenStreetMap

Map data, POI names, street geometry and land-use masks derived from
OpenStreetMap are © OpenStreetMap contributors, licensed under the
**Open Database License (ODbL) 1.0**.

* https://www.openstreetmap.org/copyright
* https://opendatacommons.org/licenses/odbl/1-0/

In-game: shown on the Credits screen and in the map panel footer.
Derivative databases (`Data/POI/poi_manifest.csv`) are distributed under ODbL.

## GUGiK / Geoportal.gov.pl

When downloaded (see `docs/W0_BLOCKERS.md` B3):

| Dataset | Use | Attribution |
|---|---|---|
| BDOT10k | buildings, roads, land cover | Główny Urząd Geodezji i Kartografii |
| NMT / NMPT / ALS | terrain, tree crowns, roof heights | Główny Urząd Geodezji i Kartografii |
| PRG | addresses, street names | Główny Urząd Geodezji i Kartografii |
| ORTO | visual reference only, never shipped as a texture | Główny Urząd Geodezji i Kartografii |

* https://www.geoportal.gov.pl/
* https://www.geoportal.gov.pl/en/data/topographic-objects-database-bdot10k/

## Gmina Chełmek / Powiat Oświęcimski

* MPZP and studium: https://chelmek.pl/miasto-i-gmina/planowanie-przestrzenne
* SIP map portal: https://sip.gison.pl/chelmek
* EGiB extracts via the powiat office

## Official pages used to verify POI facts (text only, no assets)

* https://chelmek.pl/miasto-i-gmina/edukacja
* https://chelmek.pl/miasto-i-gmina/parafie
* https://ratusz.pl/1213033/chelmek/urzad-gminy
* https://sgzoz.pl/kontakt
* https://ptys.pl/kontakt/
* http://www.restauracja-cizemka.pl/kontakt/

## Explicitly NOT used

No content from Google Maps, Google Street View, Google Places, Google
elevation, or any scraped tile/panorama. No assets, characters, HUD, missions,
music or logos from Rockstar Games / GTA. See `docs/CONTRACT.md` §8; CI enforces
the Google prohibition.

## Third-party software (planned)

| Component | Licence | Status |
|---|---|---|
| Unreal Engine 5.8 | Epic Games EULA, royalty | not yet used (B1) |
| `pyproj` (test-time cross-check only) | MIT | dev dependency |
| `three.js` (existing web build) | MIT | present in the web repo, not part of the UE build |
