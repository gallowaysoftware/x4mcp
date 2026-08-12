/**
 * The board store: the one place the pure pieces are wired to the world.
 *
 * Everything that can be decided without a browser was decided somewhere else —
 * `reducer.ts` folds the event log, `freshness.ts` renders the stamp,
 * `seen.ts` runs the amber lifecycle, `arming.ts` answers whether the alarm is
 * really armed. This class owns exactly the things a test cannot hold: the
 * clock, the EventSource, `fetch`, `localStorage`, the Notification API and the
 * audio device.
 *
 * It also owns THE clock — design §6: one clock, no per-panel drift. Every age,
 * stamp and 1 Hz parse tick on the board derives from `nowMS`, which one
 * interval advances once a second. A panel that starts its own timer is a panel
 * whose numbers disagree with the panel next to it.
 */

import { fetchState, fetchVitals, refreshSave } from '../api';
import {
	armingBadges,
	armingComplete,
	armingConditionKeys,
	armingItems,
	CHIME_INTENT_KEY,
	parseChimeIntent,
	resolveChime,
	type ArmingBadge,
	type ArmingItem,
	type ArmingState,
	type ChimeIntent,
	type NotifyState,
} from '../arming';
import { Chime } from '../chime';
import { EventStream } from '../events';
import { connectionState, renderFreshness, type FreshnessRender } from '../freshness';
import { legChipCopy, legConditionKeys, type LegChipCopy } from '../legs';
import {
	FreshnessStateParseError,
	FreshnessStateSchemaMismatch,
	type VitalsView,
} from '../types.gen';
import type { ConnectionState } from '../types';
import {
	beaconState,
	emptySeen,
	hasNew,
	markInteraction,
	newSince,
	observe,
	type BeaconState,
	type SeenState,
} from '../seen';
import { hasSnapshot, initialBoardState, reduce, type BoardAction, type BoardState } from '../reducer';

export class Board {
	#board = $state<BoardState>(initialBoardState());
	#nowMS = $state(Date.now());
	#connection = $state<ConnectionState>('live');
	#lastContactAtMS = $state(Date.now());
	/**
	 * Deliberately per-tab and not persisted. A reload therefore re-arms
	 * standing ambers until the first click, which is the right way round: the
	 * alternative — remembering "seen" across reloads — would let an amber that
	 * arrived while the tab was closed come up already marked as seen, and a
	 * missed alarm is a far worse failure than one extra amber beacon.
	 */
	#seen = $state<SeenState>(emptySeen(Date.now()));
	#chimeIntent = $state<ChimeIntent>('unarmed');
	#chimeUnlocked = $state(false);
	#notify = $state<NotifyState>('default');
	#drawer = $state(false);
	/** Set when the bootstrap itself fails: an empty board that says why. */
	#bootstrapError = $state<string | undefined>(undefined);

	#chime = new Chime();
	#stream: EventStream | undefined;
	#ticker: ReturnType<typeof setInterval> | undefined;
	#resyncing = false;
	#vitalsInFlight = false;

	// ---- reads -------------------------------------------------------------

	get state(): BoardState {
		return this.#board;
	}
	get nowMS(): number {
		return this.#nowMS;
	}
	get connection(): ConnectionState {
		return this.#connection;
	}
	get bootstrapError(): string | undefined {
		return this.#bootstrapError;
	}
	get published(): boolean {
		return hasSnapshot(this.#board);
	}
	get drawerOpen(): boolean {
		return this.#drawer;
	}

	get vitals(): VitalsView {
		return this.#board.vitals;
	}

	freshness: FreshnessRender = $derived(
		renderFreshness({
			freshness: this.#board.vitals.freshness,
			connection: this.#connection,
			nowMS: this.#nowMS,
			parseStartedAtMS: this.#board.parseStartedAtMS,
			lastContactAtMS: this.#lastContactAtMS,
		}),
	);

	legs: LegChipCopy[] = $derived(this.#board.vitals.legs.map(legChipCopy));

	arming: ArmingState = $derived({
		watchDirs: this.#board.vitals.freshness.watch_dirs,
		chime: resolveChime(this.#chimeIntent, this.#chimeUnlocked),
		notify: this.#notify,
	});

	armingItems: ArmingItem[] = $derived(armingItems(this.arming));
	armingBadges: ArmingBadge[] = $derived(armingBadges(this.arming));

	/**
	 * design §7: the checklist owns the lane until every item is answered, and
	 * comes back only on a regression. Nothing remembers "dismissed" — the card
	 * is a pure function of whether the alarm is actually armed, which is the
	 * only way a revoked permission can bring it back.
	 */
	showChecklist: boolean = $derived(!armingComplete(this.arming));

	/**
	 * The amber condition set this build can honestly produce. Alert-derived
	 * ambers (idle ships, build stalls, sightings) join it in S9 — the mechanism
	 * is here now precisely so that they land in a board whose resting state is
	 * already near-zero-chroma.
	 */
	amberConditions: string[] = $derived([
		...legConditionKeys(this.#board.vitals.legs),
		...armingConditionKeys(this.arming),
		...systemConditionKeys(this.#board, this.#connection),
	]);

	newAmbers: string[] = $derived(newSince(this.#seen, this.amberConditions));

	/**
	 * design §2: red for unacked reds, amber ONLY for ambers newer than the last
	 * board interaction, gray otherwise. The lane has no reds until S9, so this
	 * board's beacon is gray or amber — and that is the correct resting state,
	 * not a stub.
	 */
	beacon: BeaconState = $derived(beaconState(this.unackedReds, this.newAmbers.length));

	/** No alert engine yet (S9); stated as a named zero rather than hidden in a literal. */
	get unackedReds(): number {
		return 0;
	}

	/**
	 * Does this count have something new behind it? Standing counts carry no
	 * severity suffix (design §2) — a count earns its glyph only while its
	 * condition set holds something the player has not seen. Those sets are
	 * populated by the rules engine in S9; until then every count is standing,
	 * which is exactly why the resting board is quiet.
	 */
	countHasNew(prefix: string): boolean {
		return hasNew(
			this.#seen,
			this.amberConditions.filter((k) => k.startsWith(prefix)),
		);
	}

	// ---- lifecycle ---------------------------------------------------------

	start(): void {
		this.#chimeIntent = parseChimeIntent(readStorage(CHIME_INTENT_KEY));
		this.#notify = notifyState();
		// Re-derive whether audio can actually play, every boot. A previous
		// session's "armed" proves nothing about this page: autoplay policy is
		// per-document and a browser restart resets it.
		if (this.#chimeIntent === 'armed') void this.#chime.unlock().then((ok) => (this.#chimeUnlocked = ok));

		this.#stream = new EventStream(
			{
				onOpen: () => this.#dispatch({ kind: 'connected' }),
				onEvent: (event) => this.#dispatch({ kind: 'event', event, atMS: Date.now() }),
				onClosed: () => this.#dispatch({ kind: 'disconnected' }),
			},
			this.#nowMS,
		);

		void this.#bootstrap();
		this.#ticker = setInterval(() => this.#tick(), 1000);
	}

	stop(): void {
		if (this.#ticker !== undefined) clearInterval(this.#ticker);
		this.#ticker = undefined;
		this.#stream?.close();
		this.#stream = undefined;
		this.#chime.close();
	}

	/**
	 * The 1 Hz tick: advance the clock, sample the stream's liveness, run the
	 * two-stage silence ladder, and re-observe the amber set. Text swaps only —
	 * design §8 keeps every one of these out of CSS.
	 */
	#tick(): void {
		const now = Date.now();
		this.#nowMS = now;
		if (this.#stream !== undefined) this.#lastContactAtMS = this.#stream.sample(now);
		this.#connection = connectionState(this.#board.silence, (now - this.#lastContactAtMS) / 1000);
		this.#seen = observe(this.#seen, this.amberConditions, now);
	}

	// ---- interactions ------------------------------------------------------

	/**
	 * design §2: ambers need no ack — any board interaction marks the current
	 * amber set seen. Wired to a capture-phase listener on the whole board, so
	 * every click and keystroke counts and no component has to remember to.
	 */
	markInteraction(): void {
		const now = Date.now();
		this.#seen = markInteraction(observe(this.#seen, this.amberConditions, now), now);
	}

	openDrawer(): void {
		this.#drawer = true;
	}
	closeDrawer(): void {
		this.#drawer = false;
	}
	toggleDrawer(): void {
		this.#drawer = !this.#drawer;
	}

	/** The arming gesture: this click is what unlocks audio, so it plays once. */
	async armChime(): Promise<void> {
		const ok = await this.#chime.unlock();
		this.#chimeUnlocked = ok;
		this.#chimeIntent = 'armed';
		writeStorage(CHIME_INTENT_KEY, 'armed');
		if (ok) this.#chime.play();
	}

	muteChime(): void {
		this.#chimeIntent = 'muted';
		writeStorage(CHIME_INTENT_KEY, 'muted');
	}

	async requestNotifications(): Promise<void> {
		if (!('Notification' in globalThis)) {
			this.#notify = 'unavailable';
			return;
		}
		try {
			await Notification.requestPermission();
		} catch {
			// Older browsers take a callback and reject the promise form; the
			// permission is re-read either way.
		}
		this.#notify = notifyState();
	}

	async refresh(): Promise<void> {
		try {
			await refreshSave();
		} catch {
			// The stream is what reports outcomes; a failed kick shows up as a
			// board that did not change, which is the honest reading.
		}
	}

	// ---- plumbing ----------------------------------------------------------

	#dispatch(action: BoardAction): void {
		this.#board = reduce(this.#board, action);
		this.#drain();
	}

	/**
	 * Perform the work the reducer asked for. The flags are cleared here rather
	 * than on the response, so an event arriving mid-flight cannot queue a
	 * second identical refetch.
	 */
	#drain(): void {
		if (this.#board.buildChanged) {
			// tech-design §2: assets and binary ship together, so the only skew
			// that can exist is a tab left open across a restart. Reloading is
			// cheaper than versioning every path.
			location.reload();
			return;
		}
		if (this.#board.needsResync && !this.#resyncing) {
			this.#board = { ...this.#board, needsResync: false };
			void this.#bootstrap();
			return;
		}
		if (this.#board.needsVitals && !this.#vitalsInFlight) {
			this.#board = { ...this.#board, needsVitals: false };
			void this.#loadVitals();
		}
	}

	async #bootstrap(): Promise<void> {
		this.#resyncing = true;
		try {
			const view = await fetchState();
			this.#bootstrapError = undefined;
			this.#dispatch({ kind: 'bootstrap', state: view, atMS: Date.now() });
			// tech-design §2: a resync refetches state AND resubscribes. The
			// browser would otherwise keep resuming from the cursor that fell
			// off the ring, and resync every single time.
			this.#stream?.connect(this.#board.lastSeq);
		} catch (err) {
			this.#bootstrapError = err instanceof Error ? err.message : String(err);
		} finally {
			this.#resyncing = false;
		}
	}

	async #loadVitals(): Promise<void> {
		this.#vitalsInFlight = true;
		try {
			const vitals = await fetchVitals();
			this.#dispatch({ kind: 'vitals', vitals });
		} catch {
			// Keep the previous view at full brightness: it is not wrong, it is
			// old, and the freshness stamp is what says so.
		} finally {
			this.#vitalsInFlight = false;
		}
	}
}

/**
 * design §7: parse failure and schema mismatch are amber SYSTEM conditions, and
 * a lost connection is amber too — design §2's infrastructure-is-never-red rule
 * means none of these may borrow the alarm colour.
 */
function systemConditionKeys(board: BoardState, connection: ConnectionState): string[] {
	const keys: string[] = [];
	const state = board.vitals.freshness.state;
	if (state === FreshnessStateParseError) keys.push('system:parse-error');
	if (state === FreshnessStateSchemaMismatch) keys.push('system:schema-mismatch');
	if (connection === 'lost') keys.push('system:connection-lost');
	return keys;
}

function notifyState(): NotifyState {
	if (!('Notification' in globalThis)) return 'unavailable';
	const permission = Notification.permission;
	return permission === 'granted' || permission === 'denied' ? permission : 'default';
}

/** localStorage throws in some privacy modes; a preference is never worth a crash. */
function readStorage(key: string): string | null {
	try {
		return localStorage.getItem(key);
	} catch {
		return null;
	}
}

function writeStorage(key: string, value: string): void {
	try {
		localStorage.setItem(key, value);
	} catch {
		// Nothing to do: the state is still correct for this session, and it
		// will be re-derived honestly on the next boot.
	}
}

export const board = new Board();
