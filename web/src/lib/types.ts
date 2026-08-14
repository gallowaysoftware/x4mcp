// Hand-kept companions to types.gen.ts. Everything here is something the Go
// types cannot express; anything that *can* live in internal/wire belongs there
// instead, where the drift gate watches it.

import {
	type Envelope,
	type LegHealth,
	type PlaythroughChanged,
	type SaveError,
	type SaveMeta,
	type SnapshotMeta,
	EventTypeHealthLeg,
	EventTypePlaythroughChanged,
	EventTypeResync,
	EventTypeSaveDetected,
	EventTypeSaveError,
	EventTypeSaveParsing,
	EventTypeSaveRetry,
	EventTypeSnapshotReady,
} from './types.gen';

/**
 * One SSE event with its payload narrowed by the event name.
 *
 * `data` is optional exactly when the payload can be undefined, because Go's
 * `json:"data,omitempty"` (internal/wire/event.go) omits the key entirely for a
 * nil payload — a resync envelope arrives as `{seq, type, at}` with no `data`
 * member at all, and a required `data` would make the real wire bytes
 * unassignable. types.typecheck.ts holds the literal that proves it.
 */
type Event<T, D> = Omit<Envelope, 'type' | 'data'> &
	{ type: T } &
	(undefined extends D ? { data?: D } : { data: D });

/**
 * BoardEvent correlates an event name with its payload. Envelope.data is `any`
 * on the Go side — the correlation is documented in internal/wire/event.go and
 * asserted here, the one seam in the pipeline no compiler checks for us. Adding
 * an event means editing both ends.
 */
export type BoardEvent =
	| Event<typeof EventTypeSaveDetected | typeof EventTypeSaveParsing | typeof EventTypeSaveRetry, SaveMeta>
	| Event<typeof EventTypeSaveError, SaveError>
	| Event<typeof EventTypeSnapshotReady, SnapshotMeta>
	| Event<typeof EventTypeHealthLeg, LegHealth>
	| Event<typeof EventTypePlaythroughChanged, PlaythroughChanged>
	| Event<typeof EventTypeResync, undefined>;

/**
 * The two freshness states the server cannot report, because they are exactly
 * the states in which the server has stopped talking (design §6): 45 s of SSE
 * silence stales the stamp, 60 s means the connection is gone. The client owns
 * them and overrides the last Freshness it heard.
 */
export type ConnectionState = 'live' | 'silent' | 'lost';
