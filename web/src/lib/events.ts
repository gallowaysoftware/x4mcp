/**
 * The EventSource manager: subscribe, resume, reconnect, and — the part that
 * matters — know when to stop believing the stream.
 *
 * ## Resume
 *
 * `EventSource` cannot set a request header on its FIRST connect, so the
 * bootstrap cursor rides as `?last_event_id=`, which the hub also accepts. On
 * every reconnect after that the browser sends the real `Last-Event-ID` header
 * from the newest `id:` it saw, and the server prefers the header — so the two
 * mechanisms compose without the client having to rewrite its URL.
 *
 * ## Liveness, and an honest gap
 *
 * design §6 measures silence against the hub's 15 s heartbeat. That heartbeat
 * is an SSE **comment** (`: heartbeat`), and comments are invisible to
 * `EventSource` — no JS event fires, by specification. So the client cannot
 * count heartbeats; it counts two other things that are just as true on the
 * v1.0 same-machine posture:
 *
 *   1. every event that arrives, and
 *   2. the connection still being OPEN, sampled once a second.
 *
 * When the x4cue process dies, its socket closes, `readyState` leaves OPEN
 * within milliseconds and the two-stage timer starts — which is exactly the
 * state design §6 says this posture means. The heartbeat still earns its keep
 * at the transport layer: it is what keeps the socket (and any proxy) from
 * idling out, and what makes a dead peer surface as a closed socket rather than
 * as an indefinite hang.
 *
 * The case this does NOT catch is a LAN client whose network drops without the
 * socket erroring — `readyState` stays OPEN and nothing arrives. Closing that
 * gap needs the heartbeat promoted from a comment to a named event, which is a
 * server change; it is recorded here rather than papered over, and it only
 * bites a posture (PRD §12.8, LAN clients) that v1.0 does not ship.
 */

import type { BoardEvent } from './types';
import type { EventType } from './types.gen';

export interface EventStreamHandlers {
	onOpen: () => void;
	onEvent: (event: BoardEvent) => void;
	onClosed: () => void;
}

/** Every event name the hub emits, so one listener per name can be attached. */
const EVENT_NAMES: readonly EventType[] = [
	'save.detected',
	'save.parsing',
	'save.retry',
	'save.error',
	'snapshot.ready',
	'health.leg',
	'playthrough.changed',
	'resync',
];

export class EventStream {
	#source: EventSource | undefined;
	#handlers: EventStreamHandlers;
	#closed = false;
	/** Wall-clock ms of the last proof the stream was alive. */
	#lastContactAtMS: number;

	constructor(handlers: EventStreamHandlers, nowMS: number = Date.now()) {
		this.#handlers = handlers;
		this.#lastContactAtMS = nowMS;
	}

	get lastContactAtMS(): number {
		return this.#lastContactAtMS;
	}

	get open(): boolean {
		return this.#source?.readyState === 1; // EventSource.OPEN
	}

	/**
	 * Open the stream at a cursor. Called once at boot and again after a resync,
	 * because a resync has just refetched state and the old cursor is stale.
	 */
	connect(afterSeq: number): void {
		this.#closed = false;
		this.#cursor = afterSeq;
		this.#teardown();
		const url = afterSeq > 0 ? `/api/events?last_event_id=${afterSeq}` : '/api/events';
		const source = new EventSource(url);
		this.#source = source;

		source.onopen = () => {
			this.#lastContactAtMS = Date.now();
			this.#handlers.onOpen();
		};
		source.onerror = () => {
			// EventSource reconnects itself on the hub's `retry: 3000`. The only
			// error worth reacting to is a terminal one, and the way to tell is
			// readyState: CLOSED (2) means it has given up.
			this.#handlers.onClosed();
			if (source.readyState === 2 && !this.#closed) this.#reconnectLater();
		};
		for (const name of EVENT_NAMES) {
			source.addEventListener(name, (e) => this.#dispatch(name, e as MessageEvent<string>));
		}
	}

	/**
	 * Sample liveness. Called from the one 1 Hz clock rather than from a timer
	 * of its own — design §6: one clock, no per-panel drift.
	 */
	sample(nowMS: number): number {
		if (this.open) this.#lastContactAtMS = nowMS;
		return this.#lastContactAtMS;
	}

	close(): void {
		this.#closed = true;
		if (this.#retry !== undefined) clearTimeout(this.#retry);
		this.#retry = undefined;
		this.#teardown();
	}

	#retry: ReturnType<typeof setTimeout> | undefined;
	#backoffMS = 1000;
	/**
	 * The newest seq we have folded in. The browser resumes its OWN reconnects
	 * from the last `id:` it saw, but a reconnect we drive is a brand-new
	 * EventSource with no memory — without this it would resubscribe from the
	 * live edge and silently skip everything the hub buffered while the process
	 * was restarting.
	 */
	#cursor = 0;

	#reconnectLater(): void {
		if (this.#retry !== undefined) return;
		const delay = this.#backoffMS;
		// Capped exponential backoff: a gaming PC that is off should not be
		// hammered, and a process that is restarting should be found quickly.
		this.#backoffMS = Math.min(this.#backoffMS * 2, 15000);
		const cursor = this.#cursor;
		this.#retry = setTimeout(() => {
			this.#retry = undefined;
			if (!this.#closed) this.connect(cursor);
		}, delay);
	}

	#dispatch(name: EventType, e: MessageEvent<string>): void {
		this.#lastContactAtMS = Date.now();
		this.#backoffMS = 1000;
		const event = parseEnvelope(name, e);
		if (event === undefined) return;
		if (event.seq > this.#cursor) this.#cursor = event.seq;
		this.#handlers.onEvent(event);
	}

	#teardown(): void {
		if (this.#source === undefined) return;
		this.#source.onopen = null;
		this.#source.onerror = null;
		this.#source.close();
		this.#source = undefined;
	}
}

/**
 * Turn one wire message into a typed envelope, or nothing.
 *
 * This is the trust boundary: everything downstream of here — the reducer, the
 * whole board — is entitled to assume its input is well formed, and it can only
 * assume that because malformed frames stop here. A board that renders
 * `undefined` because a payload was empty has failed the same way a board that
 * renders a stale number has.
 */
export function parseEnvelope(name: EventType, e: MessageEvent<string>): BoardEvent | undefined {
	let data: unknown;
	try {
		data = JSON.parse(e.data);
	} catch {
		return undefined;
	}
	if (typeof data !== 'object' || data === null) return undefined;
	const envelope = data as { seq?: unknown; type?: unknown; at?: unknown; data?: unknown };
	if (typeof envelope.seq !== 'number' || envelope.type !== name) return undefined;
	if (typeof envelope.at !== 'string') return undefined;
	// The payload correlation is the hand-kept union in types.ts; the runtime
	// shape is the server's own marshalling of internal/wire, checked in Go.
	return data as BoardEvent;
}
