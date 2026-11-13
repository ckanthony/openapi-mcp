import { useEffect, useRef, useState } from "react";
import { GameConsole, Close } from "@carbon/icons-react";
import { Button, Tag } from "@carbon/react";
import { createGame, type GameHandle, type GameStats } from "./engine";
import { pois, spawn, stats as worldStats, type Poi } from "./world";
import "./game.scss";

type PadState = { forward: number; strafe: number; run: boolean };

export function GameView({ onExit }: { onExit: () => void }) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const gameRef = useRef<GameHandle | null>(null);
  const lookRef = useRef(0);
  const padRef = useRef<PadState>({ forward: 0, strafe: 0, run: false });
  const keysRef = useRef<Set<string>>(new Set());

  const [hud, setHud] = useState<GameStats>({
    fps: 0,
    x: spawn.x,
    z: spawn.z,
    street: spawn.name,
    heading: 0,
  });
  const [nearPoi, setNearPoi] = useState<Poi | null>(null);
  const [showHelp, setShowHelp] = useState(true);
  // Without this the whole React tree unmounts on a WebGL failure and the
  // player is left staring at a black screen with no explanation -- which is
  // exactly what happened on a phone before this was added.
  const [fatal, setFatal] = useState<string | null>(null);

  // --- lifecycle --------------------------------------------------------
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    let game: GameHandle;
    try {
      game = createGame(host);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      console.error("[chelmek] createGame failed:", err);
      setFatal(message);
      return;
    }
    gameRef.current = game;
    if (!game.isReady()) setFatal("scena nie została zainicjowana");

    const interval = window.setInterval(() => {
      const s = game.getStats();
      setHud({ ...s });
      let best: Poi | null = null;
      let bestDist = 26;
      for (const p of pois) {
        const d = Math.hypot(p.x - s.x, p.z - s.z);
        if (d < bestDist) {
          bestDist = d;
          best = p;
        }
      }
      setNearPoi(best);
    }, 180);

    return () => {
      window.clearInterval(interval);
      game.dispose();
      gameRef.current = null;
    };
  }, []);

  // --- keyboard ---------------------------------------------------------
  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      keysRef.current.add(e.code);
      if (["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Space"].includes(e.code)) {
        e.preventDefault();
      }
      if (e.code === "Escape") onExit();
      if (e.code === "KeyH") setShowHelp((v) => !v);
    };
    const up = (e: KeyboardEvent) => keysRef.current.delete(e.code);
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
    };
  }, [onExit]);

  // --- per-frame input bridge ------------------------------------------
  useEffect(() => {
    let raf = 0;
    const pump = () => {
      const keys = keysRef.current;
      const pad = padRef.current;
      let forward = pad.forward;
      let strafe = pad.strafe;

      if (keys.has("KeyW") || keys.has("ArrowUp")) forward += 1;
      if (keys.has("KeyS") || keys.has("ArrowDown")) forward -= 1;
      if (keys.has("KeyD") || keys.has("ArrowRight")) strafe += 1;
      if (keys.has("KeyA") || keys.has("ArrowLeft")) strafe -= 1;

      forward = Math.max(-1, Math.min(1, forward));
      strafe = Math.max(-1, Math.min(1, strafe));

      gameRef.current?.setInput({
        forward,
        strafe,
        run: keys.has("ShiftLeft") || keys.has("ShiftRight") || pad.run,
        look: lookRef.current,
      });
      lookRef.current = 0;
      raf = requestAnimationFrame(pump);
    };
    raf = requestAnimationFrame(pump);
    return () => cancelAnimationFrame(raf);
  }, []);

  // --- touch look -------------------------------------------------------
  const dragRef = useRef<{ x: number; id: number } | null>(null);

  const onPointerDown = (e: React.PointerEvent) => {
    if ((e.target as HTMLElement).closest(".pad, .hud")) return;
    dragRef.current = { x: e.clientX, id: e.pointerId };
  };

  const onPointerMove = (e: React.PointerEvent) => {
    const drag = dragRef.current;
    if (!drag || drag.id !== e.pointerId) return;
    const dx = e.clientX - drag.x;
    drag.x = e.clientX;
    lookRef.current += dx * 0.0035;
  };

  const endDrag = () => {
    dragRef.current = null;
  };

  // --- on-screen pad (Android) -----------------------------------------
  const setPad = (next: Partial<PadState>) => {
    padRef.current = { ...padRef.current, ...next };
  };

  const padButton = (
    label: string,
    update: Partial<PadState>,
    className: string,
  ) => (
    <button
      type="button"
      className={`pad__btn ${className}`}
      aria-label={label}
      onPointerDown={(e) => {
        e.preventDefault();
        setPad(update);
      }}
      onPointerUp={() => setPad({ forward: 0, strafe: 0, run: false })}
      onPointerLeave={() => setPad({ forward: 0, strafe: 0, run: false })}
      onPointerCancel={() => setPad({ forward: 0, strafe: 0, run: false })}
    >
      {label}
    </button>
  );

  const headingDeg = ((hud.heading * 180) / Math.PI + 180) % 360;

  return (
    <div
      className="game"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
    >
      <div className="game__canvas" ref={hostRef} />

      {fatal ? (
        <div className="game__fatal" role="alert">
          <h2>Grafika 3D nie wystartowała</h2>
          <p>
            Przeglądarka nie udostępniła kontekstu WebGL, więc nie ma czego
            narysować. To ograniczenie urządzenia lub przeglądarki, nie brak
            danych o Chełmku.
          </p>
          <ul>
            <li>Włącz akcelerację sprzętową w ustawieniach przeglądarki.</li>
            <li>Zamknij inne karty z grafika 3D i odśwież stronę.</li>
            <li>Spróbuj w Chrome lub Firefoksie na komputerze.</li>
          </ul>
          <p className="game__fatal-detail">Szczegóły techniczne: {fatal}</p>
          <Button kind="ghost" size="sm" onClick={onExit}>
            Wróć do strony głównej
          </Button>
        </div>
      ) : null}

      <div className="hud">
        <div className="hud__top">
          <div className="hud__chip">
            <span className="hud__label">ulica</span>
            <span className="hud__value">{hud.street}</span>
          </div>
          <div className="hud__chip">
            <span className="hud__label">FPS</span>
            <span className="hud__value">{hud.fps}</span>
          </div>
          <div className="hud__chip">
            <span className="hud__label">kierunek</span>
            <span className="hud__value">{Math.round(headingDeg)}°</span>
          </div>
          <Button
            kind="ghost"
            size="sm"
            renderIcon={Close}
            onClick={onExit}
            className="hud__exit"
          >
            Wyjdź
          </Button>
        </div>

        {nearPoi ? (
          <div className="hud__near">
            <Tag type="cool-gray" size="sm">
              w pobliżu
            </Tag>
            <span>{nearPoi.name}</span>
            <Button
              kind="ghost"
              size="sm"
              onClick={() => gameRef.current?.teleportToPoi(nearPoi)}
            >
              Podejdź
            </Button>
          </div>
        ) : null}
      </div>

      {showHelp ? (
        <div className="hud__help">
          <div>
            <GameConsole size={20} /> <strong>W A S D</strong> — ruch ·{" "}
            <strong>Shift</strong> — bieg · <strong>przeciągnij palcem</strong> —
            rozglądanie
          </div>
          <div className="hud__help-dim">
            {worldStats.buildings} budynków · {worldStats.namedRoads} nazwanych ulic ·{" "}
            {worldStats.pois} punktów z OSM
          </div>
          <Button kind="ghost" size="sm" onClick={() => setShowHelp(false)}>
            Rozumiem
          </Button>
        </div>
      ) : null}

      <div className="pad" aria-label="Sterowanie dotykowe">
        <div className="pad__grid">
          <span />
          {padButton("▲", { forward: 1 }, "pad__btn--up")}
          <span />
          {padButton("◀", { strafe: -1 }, "pad__btn--left")}
          {padButton("●", { run: true }, "pad__btn--run")}
          {padButton("▶", { strafe: 1 }, "pad__btn--right")}
          <span />
          {padButton("▼", { forward: -1 }, "pad__btn--down")}
          <span />
        </div>
      </div>
    </div>
  );
}
