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
 * ## Liveness is BYTES RECEIVED. Nothing else counts.
 *
 * design §6 measures silence against the hub's 15 s heartbeat, and the client
 * now receives that heartbeat: it is a named event (`heartbeat`), not the SSE
 * comment it used to be, because comments fire no JS event by specification and
 * a client cannot count what the browser never hands it.
 *
 * This module therefore stamps `#lastContactAtMS` in exactly three places, and
 * every one of them is bytes arriving from the server: an event, a heartbeat,
 * and the open handshake. It used to stamp a fourth — once a second, for as
 * long as `readyState` was OPEN — and that one was not evidence of anything.
 * A socket is open until somebody closes it, and the cases where nobody does
 * are precisely the cases the silence ladder exists for:
 *
 *   - a server frozen rather than killed (SIGSTOP, a cgroup freezer, a
 *     debugger, a suspended host): TCP stays ESTABLISHED and not one byte
 *     arrives, indefinitely — no bug required anywhere for this;
 *   - a wedged hub that has stopped writing but cannot close the connection;
 *   - a LAN client whose network drops without the socket erroring (PRD §12.8).
 *
 * Measured against the old rule: five minutes — and an hour — of total silence
 * on an open socket still read as `connection = live`, with the stamp saying
 * `quicksave · 5m ago` and alert intake never pausing. The 44/45/60 s ladder was
 * right the whole time; its input was a tautology.
 */

import type { BoardEvent } from './types';
import { EventTypeHeartbeat, type EventType } from './types.gen';

export interface EventStreamHandlers {
	onOpen: () => void;
	onEvent: (event: BoardEvent) => void;
	onClosed: () => void;
}

/**
 * Every event name the hub emits that CARRIES BOARD STATE, so one listener per
 * name can be attached. The heartbeat is deliberately not in this list: it has
 * no sequence, no payload and nothing for the reducer to fold — its entire job
 * is to be a byte that arrived, and it gets its own listener in `connect`.
 */
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
	/**
	 * Wall-clock ms of the last BYTES that arrived from the server — an event, a
	 * heartbeat, or the open handshake. Never a clock tick: see the module
	 * comment for what happens when a board is allowed to prove its own liveness.
	 */
	#lastContactAtMS: number;

	constructor(handlers: EventStreamHandlers, nowMS: number = Date.now()) {
		this.#handlers = handlers;
		this.#lastContactAtMS = nowMS;
	}

	/** What the store's 1 Hz tick runs the silence ladder against. */
	get lastContactAtMS(): number {
		return this.#lastContactAtMS;
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
			// A response arrived: headers, and the hub's `retry:` line behind
			// them. Real bytes, so real contact.
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
		// The keep-alive, and the only thing on this stream that proves the
		// server is still there between saves. It is stamped and dropped: there
		// is no envelope to parse, no seq to advance, and nothing to tell the
		// reducer — the byte IS the message.
		source.addEventListener(EventTypeHeartbeat, () => {
			this.#lastContactAtMS = Date.now();
		});
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
