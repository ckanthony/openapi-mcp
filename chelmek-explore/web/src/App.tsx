import { useMemo, useState } from "react";
import {
  Button,
  Tag,
  Tile,
  InlineLoading,
  StructuredListWrapper,
  StructuredListHead,
  StructuredListBody,
  StructuredListRow,
  StructuredListCell,
} from "@carbon/react";
import {
  pois,
  stats,
  roadmap,
  schemaVersion,
  blockerCount,
  type RoadmapRow,
} from "./data";

const STATUS_TAG: Record<
  RoadmapRow["status"],
  { type: "green" | "blue" | "red" | "gray"; label: string }
> = {
  done: { type: "green", label: "gotowe" },
  partial: { type: "blue", label: "częściowo" },
  blocked: { type: "red", label: "zablokowane" },
  todo: { type: "gray", label: "nierozpoczęte" },
};

function Stat({ value, label, note }: { value: string; label: string; note?: string }) {
  return (
    <div className="stat">
      <span className="stat__value">{value}</span>
      <span className="stat__label">{label}</span>
      {note ? <span className="stat__note">{note}</span> : null}
    </div>
  );
}

export function App() {
  const [showAll, setShowAll] = useState(false);

  const visible = useMemo(
    () => (showAll ? pois : pois.slice(0, 8)),
    [showAll],
  );

  const done = roadmap.filter((r) => r.status === "done").length;

  return (
    <div className="app">
      <header className="cds--ui-shell" aria-label="Chełmek EXPLORE">
        <div className="cds--ui-shell__header">
          <span className="cds--ui-shell__name">Chełmek&nbsp;EXPLORE</span>
          <Tag type="outline" size="sm">
            SCHEMA {schemaVersion}
          </Tag>
        </div>
      </header>

      <main className="app__main">
        <section className="hero">
          <span className="hero__eyebrow">Launcher · projekt w toku</span>
          <h1>Chełmek 1:1 w Unreal Engine 5</h1>
          <p>
            Ta strona jest wyłącznie frontendem. Silnik UE5 renderuje się na
            desktopie lub na hoście GPU przez Pixel Streaming — Vercel nie
            uruchamia Unreal Engine. Dane POI poniżej pochodzą wprost z
            zamrożonego kontraktu <span className="mono">W0</span>, nie z ręcznie
            wpisanej listy.
          </p>
          <div className="hero__actions">
            <Button kind="primary" disabled href="#">
              Pobierz build desktop
            </Button>
            <Button kind="tertiary" disabled href="#">
              Pixel Streaming
            </Button>
            <InlineLoading description="Build niedostępny — bloker B1 (brak UE / GPU)" />
          </div>
        </section>

        <section className="stat-grid" aria-label="Stan projektu">
          <Stat
            value={String(stats.poiCount)}
            label="POI w kontrakcie"
            note={`${stats.verifiedCount} potwierdzonych`}
          />
          <Stat
            value={String(stats.enterableCount)}
            label="z wejściem do środka"
            note="lista na krok W3"
          />
          <Stat
            value={String(stats.brandCount)}
            label="marek w manifeście"
            note={`${stats.fallbackCount} fallback / ${stats.licensedCount} licensed`}
          />
          <Stat
            value={`${done}/${roadmap.length}`}
            label="kroków roadmapy"
            note="W0 ukończone"
          />
          <Stat value={String(blockerCount)} label="blokerów" note="patrz niżej" />
        </section>

        <section className="section">
          <div className="section__head">
            <h2>Punkty w mieście</h2>
            <Button
              kind="ghost"
              size="sm"
              onClick={() => setShowAll((v) => !v)}
            >
              {showAll ? "Pokaż mniej" : `Pokaż wszystkie (${pois.length})`}
            </Button>
          </div>
          <p className="section__hint">
            Współrzędne w układzie UE (centymetry, X = północ, Y = wschód) są
            wyliczane przez <span className="mono">Scripts/geo_to_ue.py</span>.
            Wiersze oznaczone jako niepotwierdzone to seed z poprzedniej wersji
            webowej i wymagają weryfikacji w kroku W5.
          </p>

          <StructuredListWrapper isCondensed aria-label="Lista POI">
            <StructuredListHead>
              <StructuredListRow head>
                <StructuredListCell head>Punkt</StructuredListCell>
                <StructuredListCell head>Kategoria</StructuredListCell>
                <StructuredListCell head>Współrzędne UE [cm]</StructuredListCell>
                <StructuredListCell head>Branding</StructuredListCell>
                <StructuredListCell head>Źródło</StructuredListCell>
              </StructuredListRow>
            </StructuredListHead>
            <StructuredListBody>
              {visible.map((p) => (
                <StructuredListRow key={p.poiId}>
                  <StructuredListCell>
                    <span className="poi-cell">
                      <span className="poi-cell__name">{p.name}</span>
                      <span className="poi-cell__meta">
                        {p.street
                          ? `${p.street} ${p.housenumber}`.trim()
                          : p.poiId}
                      </span>
                    </span>
                  </StructuredListCell>
                  <StructuredListCell>{p.category}</StructuredListCell>
                  <StructuredListCell>
                    <span className="mono">
                      {p.ueX}, {p.ueY}
                    </span>
                  </StructuredListCell>
                  <StructuredListCell>
                    <Tag
                      type={p.branding === "licensed" ? "green" : "cool-gray"}
                      size="sm"
                    >
                      {p.branding}
                    </Tag>
                  </StructuredListCell>
                  <StructuredListCell>
                    <Tag
                      type={p.verified ? "green" : "warm-gray"}
                      size="sm"
                    >
                      {p.verified ? "zweryfikowane" : "seed"}
                    </Tag>
                  </StructuredListCell>
                </StructuredListRow>
              ))}
            </StructuredListBody>
          </StructuredListWrapper>
        </section>

        <section className="section">
          <div className="section__head">
            <h2>Roadmapa</h2>
            <span className="section__hint">
              Czytane bezpośrednio z <span className="mono">ROADMAP_STATUS.md</span>
            </span>
          </div>
          <div className="roadmap">
            {roadmap.map((r) => (
              <div className="roadmap__row" key={r.step}>
                <span className="roadmap__step">{r.step}</span>
                <span className="roadmap__name">{r.name}</span>
                <Tag type={STATUS_TAG[r.status].type} size="sm">
                  {STATUS_TAG[r.status].label}
                </Tag>
              </div>
            ))}
          </div>
        </section>

        <section className="section">
          <div className="section__head">
            <h2>Co blokuje dalszą pracę</h2>
          </div>
          <Tile className="blocker">
            <strong>B1 — brak Unreal Engine 5.8 i GPU.</strong> Bez tego nie
            powstanie ani jedna mapa, wnętrze ani panorama. Potrzebny warsztat z
            UE oraz host GPU do streamingu.
          </Tile>
          <Tile className="blocker">
            <strong>B3 — brak sieci do Geoportalu.</strong> Jedno pobranie
            BDOT10k, NMT i PRG odblokowuje kroki W1, W2 i W5 jednocześnie.
          </Tile>
          <Tile className="blocker">
            <strong>B2 — brak zgód na znaki towarowe.</strong> Wszystkie{" "}
            {stats.brandCount} marek działa w trybie <span className="mono">fallback</span>.
            Build licencjonowany jest technicznie niemożliwy, dopóki każdy wiersz
            nie ma numeru zgody i daty wygaśnięcia.
          </Tile>
        </section>
      </main>

      <footer className="footer">
        <p>
          Dane: © OpenStreetMap contributors (ODbL 1.0) · GUGiK / Geoportal.gov.pl
          · Gmina Chełmek. Zero zasobów Google Maps i Street View.
        </p>
        <p>
          UI: Carbon Design System © IBM, Apache-2.0. Silnik docelowy: Unreal
          Engine 5.8 (EULA Epic Games) — nieużywany w tym buildzie.
        </p>
      </footer>
    </div>
  );
}
