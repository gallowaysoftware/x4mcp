/**
 * The board as a pure reducer over one ordered event log (tech-design §2).
 *
 * Everything the browser knows arrives as either a `/api/state` bootstrap or an
 * SSE envelope, and both fold into this one function. Keeping it pure buys the
 * two properties that matter for a screen nobody watches while it fails:
 *
 *   - the out-of-order, duplicate, gap and server-restart cases are testable
 *     without a network;
 *   - "refetch this" is a *flag in the state*, not a fetch buried in a handler,
 *     so a resync is something the store performs rather than something the
 *     reducer does behind its own back.
 *
 * No DOM, no clock, no fetch: `atMS` is always passed in.
 */

import {
	EventTypeHealthLeg,
	EventTypePlaythroughChanged,
	EventTypeResync,
	EventTypeSaveDetected,
	EventTypeSaveError,
	EventTypeSaveParsing,
	EventTypeSaveRetry,
	EventTypeSnapshotReady,
	FreshnessStateCurrent,
	FreshnessStateParseError,
	FreshnessStateParsing,
	FreshnessStateRetrying,
	FreshnessStateRollback,
	FreshnessStateSchemaMismatch,
	FreshnessStateStartup,
	LegCanon,
	LegHum,
	LegRefrain,
	LegSave,
	SaveErrorKindSchema,
	type BuildInfo,
	type Freshness,
	type Leg,
	type LegHealth,
	type SaveMeta,
	type SilencePolicy,
	type SnapshotMeta,
	type StateView,
	type VitalsView,
	type WatchHealth,
} from './types.gen';
import type { BoardEvent } from './types';

export interface BoardState {
	/** The binary this tab is talking to; absent until the first bootstrap lands. */
	build?: BuildInfo;
	vitals: VitalsView;
	/** The published parse. Absent means no save has been read yet — not zero ships. */
	snapshot?: SnapshotMeta;
	watch?: WatchHealth;
	silence: SilencePolicy;
	/** The last file the watcher noticed, which is not yet a parse. */
	detected?: SaveMeta;
	playthroughLabel?: string;

	/** The reconnect cursor: the newest envelope seq folded in. */
	lastSeq: number;
	/** True between opening a stream and seeing its first event. */
	awaitingFirstEvent: boolean;
	connected: boolean;

	/**
	 * Client wall-clock at the moment the current parse was announced. The 1 Hz
	 * progress blocks step against this, never against a server timestamp — the
	 * two clocks are not the same clock.
	 */
	parseStartedAtMS?: number;

	/**
	 * Client wall-clock at the moment the current freshness block ARRIVED. The
	 * save's age is counted from here plus the server's own `age_s`, because
	 * `modified_at` is a server timestamp read against a client clock and the
	 * two clocks are not the same clock: a client an hour behind would render a
	 * forty-minute-old save as `just now` and never escalate it.
	 */
	freshnessAtMS: number;

	/** Work the store owes the server, drained by dispatching the answer back. */
	needsResync: boolean;
	needsVitals: boolean;
	/** The bootstrap reported a different binary than the one this tab booted against. */
	buildChanged: boolean;

	/** Stream hygiene, surfaced in the health drawer rather than swallowed. */
	duplicates: number;
	gaps: number;
	resyncs: number;
}

export type BoardAction =
	| { kind: 'bootstrap'; state: StateView; atMS: number }
	| { kind: 'connected' }
	| { kind: 'disconnected' }
	| { kind: 'event'; event: BoardEvent; atMS: number }
	| { kind: 'vitals'; vitals: VitalsView; atMS: number };

/** design §6's startup state: watching, and honest that it has seen nothing. */
export function initialBoardState(): BoardState {
	return {
		vitals: {
			freshness: { state: FreshnessStateStartup, watch_dirs: [] },
			legs: [],
			// EVERY count is absent, not 0. Nothing has been parsed, so nobody has
			// counted anything — and `fleet: 0` here is a claim about the player's
			// empire made by a board that has not read a save yet. The wire types
			// are pointers for exactly this reason.
			counts: {},
		},
		silence: { heartbeat_s: 15, stale_s: 45, lost_s: 60 },
		// No freshness block has arrived yet; startup carries no save, so there
		// is nothing to date. The first bootstrap stamps it.
		freshnessAtMS: 0,
		lastSeq: 0,
		awaitingFirstEvent: false,
		connected: false,
		needsResync: false,
		needsVitals: false,
		buildChanged: false,
		duplicates: 0,
		gaps: 0,
		resyncs: 0,
	};
}

/**
 * Has a snapshot ever been published? It is what the board says INSTEAD of a
 * number when it has none — the difference between "no save has been parsed
 * yet" and "this save parsed and the number was not in it", which are two
 * different admissions and want two different tooltips (design §3). The
 * presence of the number itself is now the wire's own answer, not this one.
 */
export function hasSnapshot(state: BoardState): boolean {
	return state.snapshot !== undefined;
}

export function reduce(state: BoardState, action: BoardAction): BoardState {
	switch (action.kind) {
		case 'bootstrap': {
			const view = action.state;
			return {
				...state,
				build: view.build,
				vitals: view.vitals,
				snapshot: view.snapshot,
				watch: view.watch,
				silence: view.silence,
				lastSeq: view.last_event_seq,
				freshnessAtMS: action.atMS,
				needsResync: false,
				needsVitals: false,
				// A parse already in flight when we arrived: we did not see it
				// start, so the blocks step from now rather than inventing an
				// elapsed time we never observed.
				parseStartedAtMS:
					view.vitals.freshness.state === FreshnessStateParsing ? action.atMS : undefined,
				buildChanged: state.build !== undefined && state.build.hash !== view.build.hash,
			};
		}
		case 'connected':
			return { ...state, connected: true, awaitingFirstEvent: true };
		case 'disconnected':
			return { ...state, connected: false };
		case 'vitals':
			return { ...state, vitals: action.vitals, freshnessAtMS: action.atMS, needsVitals: false };
		case 'event':
			return reduceEvent(state, action.event, action.atMS);
	}
}

/**
 * Sequence discipline. One TCP stream is ordered, so the only ways an event can
 * arrive out of turn are across a reconnect — the hub replays from its ring —
 * or because the process restarted and started counting from 1 again. Each case
 * has a different right answer and none of them is "apply it and hope".
 */
function reduceEvent(state: BoardState, event: BoardEvent, atMS: number): BoardState {
	let next = state;
	const seq = event.seq;

	if (next.awaitingFirstEvent) {
		next = { ...next, awaitingFirstEvent: false };
		if (seq <= next.lastSeq) {
			// The stream we just joined is numbering below where we left off:
			// this is a different process. Seq is process-monotonic, not a
			// durable cursor, so everything we hold may be from the old one —
			// take the new numbering and refetch from scratch.
			next = { ...next, lastSeq: 0, needsResync: true, resyncs: next.resyncs + 1 };
		}
	}

	if (seq <= next.lastSeq) {
		// A replayed envelope we have already folded in. Idempotence by
		// dropping it is cheaper and safer than making every payload
		// idempotent, and the count keeps it visible in the drawer.
		return { ...next, duplicates: next.duplicates + 1 };
	}

	if (next.lastSeq > 0 && seq > next.lastSeq + 1) {
		// A hole. The ring buffer moved past us or a write was lost; either
		// way the cheap, correct repair is a refetch, never a guess.
		next = { ...next, gaps: next.gaps + 1, needsResync: true };
	}

	next = { ...next, lastSeq: seq };
	return applyPayload(next, event, atMS);
}

function applyPayload(state: BoardState, event: BoardEvent, atMS: number): BoardState {
	switch (event.type) {
		case EventTypeSaveDetected:
			// Detection is not a freshness state (design §6 has none): the board
			// keeps showing the current save until a parse actually starts.
			return { ...state, detected: event.data };
		case EventTypeSaveParsing:
			return {
				...state,
				detected: event.data,
				parseStartedAtMS: atMS,
				freshnessAtMS: atMS,
				vitals: withFreshness(state.vitals, {
					state: FreshnessStateParsing,
					save: event.data,
				}),
			};
		case EventTypeSaveRetry:
			return {
				...state,
				detected: event.data,
				freshnessAtMS: atMS,
				vitals: withFreshness(state.vitals, {
					state: FreshnessStateRetrying,
					save: event.data,
					attempt: event.data.attempt ?? 1,
				}),
			};
		case EventTypeSaveError:
			return {
				...state,
				parseStartedAtMS: undefined,
				freshnessAtMS: atMS,
				vitals: withFreshness(state.vitals, {
					state:
						event.data.kind === SaveErrorKindSchema
							? FreshnessStateSchemaMismatch
							: FreshnessStateParseError,
					save: event.data.save ?? state.vitals.freshness.save,
					error: event.data,
				}),
			};
		case EventTypeSnapshotReady: {
			const meta = event.data;
			return {
				...state,
				snapshot: meta,
				parseStartedAtMS: undefined,
				freshnessAtMS: atMS,
				// Views are fetched, never pushed (tech-design §1.3): the event
				// is a wake-up and this flag is the store's to-do list.
				needsVitals: true,
				vitals: withFreshness(state.vitals, {
					state: meta.rollback === true ? FreshnessStateRollback : FreshnessStateCurrent,
					save: meta.save,
					parsed_at: meta.parsed_at,
					parse_ms: meta.parse_ms,
				}),
			};
		}
		case EventTypeHealthLeg:
			return { ...state, vitals: { ...state.vitals, legs: upsertLeg(state.vitals.legs, event.data) } };
		case EventTypePlaythroughChanged:
			return {
				...state,
				playthroughLabel: event.data.label,
				needsVitals: true,
				// A new empire: nothing is compared across the boundary, so the
				// delta and the sparkline are not "0", they are gone.
				vitals: { ...state.vitals, credits_delta: undefined, credits_series: undefined },
			};
		case EventTypeResync:
			return { ...state, needsResync: true, resyncs: state.resyncs + 1 };
	}
}

/**
 * Replace the freshness block from an SSE payload while keeping the fields only
 * `/api/views/vitals` carries. `watch_dirs`, `median_parse_ms` and
 * `autosaves_overdue` describe the WATCHER, not this save; dropping them would
 * blank the startup copy's directory and flatten the parse-progress blocks the
 * moment a parse began. Everything else — `error`, `attempt`, `parse_ms` — is
 * about one particular save and is rebuilt from scratch, so a stale attempt
 * counter can never survive into a healthy state.
 */
function withFreshness(vitals: VitalsView, patch: Omit<Freshness, 'watch_dirs'>): VitalsView {
	const base = vitals.freshness;
	return {
		...vitals,
		freshness: {
			watch_dirs: base.watch_dirs,
			median_parse_ms: base.median_parse_ms,
			autosaves_overdue: base.autosaves_overdue,
			...patch,
		},
	};
}

/**
 * Legs render in a fixed order — the MFD rule (design §4): no field ever moves
 * in response to data, and a chip row that reshuffles when refrain answers
 * first is a row nobody can read by memory.
 */
const LEG_ORDER: readonly Leg[] = [LegSave, LegHum, LegCanon, LegRefrain];

function upsertLeg(legs: readonly LegHealth[], next: LegHealth): LegHealth[] {
	const merged = legs.some((l) => l.leg === next.leg)
		? legs.map((l) => (l.leg === next.leg ? next : l))
		: [...legs, next];
	return [...merged].sort((a, b) => legRank(a.leg) - legRank(b.leg));
}

function legRank(leg: Leg): number {
	const i = LEG_ORDER.indexOf(leg);
	return i < 0 ? LEG_ORDER.length : i;
}
