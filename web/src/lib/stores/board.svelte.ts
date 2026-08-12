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
	prime,
	type BeaconState,
	type SeenState,
} from '../seen';
import { hasSnapshot, initialBoardState, reduce, type BoardAction, type BoardState } from '../reducer';

/** The retry ladder for a failed bootstrap or refetch: 1 s, doubling, capped. */
const RETRY_MIN_MS = 1000;
const RETRY_MAX_MS = 15000;

export class Board {
	#board = $state<BoardState>(initialBoardState());
	#nowMS = $state(Date.now());
	#connection = $state<ConnectionState>('live');
	#lastContactAtMS = $state(Date.now());
	/**
	 * Deliberately per-tab and not persisted: what the player has been shown is
	 * a fact about this session, not about this playthrough.
	 *
	 * It stays UNPRIMED until the first bootstrap lands, and is primed with
	 * whatever is standing at that moment (`#bootstrap`). That is the difference
	 * between an instrument and a decoration: an amber that was already true
	 * when the tab opened is not an arrival, and a board opened once on a second
	 * monitor and never touched again would otherwise sit amber forever —
	 * because the one gesture that clears it is the gesture this product says
	 * will not happen.
	 */
	#seen = $state<SeenState>(emptySeen(Date.now()));
	#chimeIntent = $state<ChimeIntent>('unarmed');
	/**
	 * What the audio device will do RIGHT NOW, re-read every tick rather than
	 * remembered from the arming click. Browsers suspend an AudioContext behind
	 * a background tab or an audio-device change, and a cached "unlocked" is
	 * precisely the silently disarmed alarm design §7 calls the worst honesty
	 * failure this product can have.
	 */
	#chimeUnlocked = $state(false);
	#chimeAvailable = $state(true);
	#notify = $state<NotifyState>('default');
	#drawer = $state(false);
	/** Set when the bootstrap itself fails: an empty board that says why. */
	#bootstrapError = $state<string | undefined>(undefined);

	#chime = new Chime();
	#stream: EventStream | undefined;
	#ticker: ReturnType<typeof setInterval> | undefined;
	#resyncing = false;
	#vitalsInFlight = false;
	/** Set while a `connect()` WE asked for is in flight, so its `onopen` is not read as a reconnect. */
	#connecting = false;
	#streamStarted = false;
	/** Earliest wall-clock at which the tick may retry the work that failed. */
	#retryAtMS = 0;
	#retryBackoffMS = RETRY_MIN_MS;

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
			freshnessAtMS: this.#board.freshnessAtMS,
			lastContactAtMS: this.#lastContactAtMS,
		}),
	);

	legs: LegChipCopy[] = $derived(this.#board.vitals.legs.map(legChipCopy));

	arming: ArmingState = $derived({
		watchDirs: this.#board.vitals.freshness.watch_dirs,
		chime: resolveChime(this.#chimeIntent, this.#chimeUnlocked, this.#chimeAvailable),
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
		this.#chimeAvailable = this.#chime.available;
		// Re-derive whether audio can actually play, every boot. A previous
		// session's "armed" proves nothing about this page: autoplay policy is
		// per-document and a browser restart resets it.
		if (this.#chimeIntent === 'armed') void this.#chime.unlock().then(() => this.#readChime());

		this.#stream = new EventStream(
			{
				onOpen: () => this.#opened(),
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
	 * The 1 Hz tick: advance the clock, sample the stream's liveness, re-read
	 * the audio device, run the two-stage silence ladder, re-observe the amber
	 * set, and retry whatever the last attempt failed to do. Text swaps only —
	 * design §8 keeps every one of these out of CSS.
	 */
	#tick(): void {
		const now = Date.now();
		this.#nowMS = now;
		if (this.#stream !== undefined) this.#lastContactAtMS = this.#stream.sample(now);
		this.#readChime();
		this.#connection = connectionState(this.#board.silence, (now - this.#lastContactAtMS) / 1000);
		this.#seen = observe(this.#seen, this.amberConditions, now);
		this.#retryPending(now);
	}

	/** Ask the device, never the memory of a click (design §7). */
	#readChime(): void {
		this.#chimeUnlocked = this.#chime.unlocked;
		this.#chimeAvailable = this.#chime.available;
	}

	/**
	 * The board's to-do list, retried on a capped backoff.
	 *
	 * Nothing else retries: a failed bootstrap used to leave a tab that looked
	 * alive and was permanently empty, recoverable only by a manual reload —
	 * which is the most likely sequence there is for a board that autostarts on
	 * a second monitor before the service is up.
	 */
	#retryPending(nowMS: number): void {
		if (nowMS < this.#retryAtMS) return;
		if (this.#bootstrapError !== undefined || this.#board.needsResync) {
			this.#backOff(nowMS);
			void this.#bootstrap();
			return;
		}
		if (this.#board.needsVitals) {
			this.#backOff(nowMS);
			void this.#loadVitals();
		}
	}

	#backOff(nowMS: number): void {
		this.#retryAtMS = nowMS + this.#retryBackoffMS;
		// A gaming PC that is off should not be hammered; a process that is
		// restarting should be found quickly. Same shape as the stream's own.
		this.#retryBackoffMS = Math.min(this.#retryBackoffMS * 2, RETRY_MAX_MS);
	}

	#retriesSucceeded(): void {
		this.#retryAtMS = 0;
		this.#retryBackoffMS = RETRY_MIN_MS;
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
		this.#readChime();
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
	 * A stream that opened. The FIRST open is the one we asked for at boot;
	 * every one after it is a reconnect, and a reconnect may well be a different
	 * process — the hub sends nothing at all when a client's cursor is ahead of
	 * the new process's sequence, so nothing would ever tell us. Refetching
	 * `/api/state` is what turns a restart into a board that is current instead
	 * of a board showing a dead process's numbers with a live process's
	 * liveness. `#connecting` is what keeps that from being a loop: a connect we
	 * drove ourselves is not news.
	 */
	#opened(): void {
		this.#dispatch({ kind: 'connected' });
		if (this.#connecting) {
			this.#connecting = false;
			return;
		}
		void this.#bootstrap();
	}

	/**
	 * Perform the work the reducer asked for. The flags are cleared by the
	 * RESPONSE (the reducer clears them when the answer is dispatched), never
	 * before the request — a refetch that fails must leave its to-do item in
	 * place, or the board silently keeps stale data forever. The in-flight
	 * guards live in the two loaders, so a second event arriving mid-flight
	 * cannot queue a duplicate request either.
	 */
	#drain(): void {
		if (this.#board.buildChanged) {
			// tech-design §2: assets and binary ship together, so the only skew
			// that can exist is a tab left open across a restart. Reloading is
			// cheaper than versioning every path.
			location.reload();
			return;
		}
		if (this.#board.needsResync) {
			void this.#bootstrap();
			return;
		}
		if (this.#board.needsVitals) void this.#loadVitals();
	}

	async #bootstrap(): Promise<void> {
		if (this.#resyncing) return;
		this.#resyncing = true;
		try {
			const view = await fetchState();
			this.#bootstrapError = undefined;
			this.#retriesSucceeded();
			this.#dispatch({ kind: 'bootstrap', state: view, atMS: Date.now() });
			// The boot inventory (design §2): whatever is amber in the first
			// state this tab ever sees was already standing when it opened, so
			// it is not an arrival and must not light the ambient channel.
			if (!this.#seen.primed) this.#seen = prime(this.#seen, this.amberConditions, Date.now());
			// tech-design §2: a resync refetches state AND resubscribes. The
			// browser would otherwise keep resuming from the cursor that fell
			// off the ring, and resync every single time.
			this.#connect();
		} catch (err) {
			this.#bootstrapError = err instanceof Error ? err.message : String(err);
			// The stream does not depend on the bootstrap having worked. It is
			// the thing that notices the server coming back, and a board that
			// never subscribed because one fetch failed at boot is a dead alarm
			// that looks like an alarm.
			if (!this.#streamStarted) this.#connect();
		} finally {
			this.#resyncing = false;
		}
	}

	#connect(): void {
		this.#streamStarted = true;
		this.#connecting = true;
		this.#stream?.connect(this.#board.lastSeq);
	}

	async #loadVitals(): Promise<void> {
		if (this.#vitalsInFlight) return;
		this.#vitalsInFlight = true;
		try {
			const vitals = await fetchVitals();
			this.#retriesSucceeded();
			this.#dispatch({ kind: 'vitals', vitals, atMS: Date.now() });
		} catch {
			// Keep the previous view at full brightness: it is not wrong, it is
			// old, and the freshness stamp is what says so. `needsVitals` stays
			// set, so the tick tries again.
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
