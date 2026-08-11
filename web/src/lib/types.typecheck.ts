/**
 * The compile-time consumer of the hand-kept half of the wire types.
 *
 * types.gen.ts is watched by CI's tygo drift gate. types.ts is not generated
 * and cannot be — it states the event-name -> payload correlation that Go's
 * `Data any` erases — so nothing mechanical watched it, and nothing in src/
 * imported it either: BoardEvent was wrong for `resync` from the day it was
 * written and every gate stayed green.
 *
 * This file is that gate. It ships nothing (no module imports it, so Vite never
 * bundles it) but tsconfig's `include` picks it up, so `npm run check` fails
 * here. It fails two ways on purpose:
 *
 *   1. one literal per arm — a payload that stops matching its event name, or a
 *      required/optional mismatch against what the server actually sends, stops
 *      being assignable;
 *   2. an exhaustive switch with a `never` default — an event added to
 *      internal/wire and to BoardEvent, but not handled here, stops compiling.
 *
 * When you add an event: internal/wire/event.go, then BoardEvent in types.ts,
 * then both halves below.
 */

import type { BoardEvent } from './types';
import {
	EventTypeHealthLeg,
	EventTypePlaythroughChanged,
	EventTypeResync,
	EventTypeSaveDetected,
	EventTypeSaveError,
	EventTypeSaveParsing,
	EventTypeSaveRetry,
	EventTypeSnapshotReady,
	LegSave,
	SaveErrorKindParse,
	type SaveMeta,
} from './types.gen';

const at = '2026-08-10T14:30:00Z';

const save: SaveMeta = {
	path: '/saves/quicksave.xml.gz',
	name: 'quicksave',
	size_bytes: 812345678,
	modified_at: at,
};

/** One envelope of every shape the hub can push, exactly as it arrives. */
export const boardEventSamples: BoardEvent[] = [
	{ seq: 1, at, type: EventTypeSaveDetected, data: save },
	{ seq: 2, at, type: EventTypeSaveParsing, data: save },
	{ seq: 3, at, type: EventTypeSaveRetry, data: { ...save, attempt: 2 } },
	{ seq: 4, at, type: EventTypeSaveError, data: { kind: SaveErrorKindParse, detail: 'unexpected EOF', save } },
	{
		seq: 5,
		at,
		type: EventTypeSnapshotReady,
		data: {
			game_guid: '00000000-0000-4000-8000-000000000000',
			save,
			schema_version: 1,
			parsed_at: at,
			parse_ms: 187,
			game_time_s: 591711.419,
			save_date: '2026-08-10T14:29:58Z',
			game_version: '7.50',
			player_name: 'Test Pilot',
		},
	},
	{ seq: 6, at, type: EventTypeHealthLeg, data: { leg: LegSave, up: true } },
	{ seq: 7, at, type: EventTypePlaythroughChanged, data: { game_guid: '00000000-0000-4000-8000-000000000000' } },
	// resync carries no payload: Go omits the key, so there is no `data` member
	// here at all. This literal is the one that used to fail.
	{ seq: 8, at, type: EventTypeResync },
];

/**
 * Narrowing every arm to its payload. The `never` default is the exhaustiveness
 * check; the property reads are the correlation check.
 */
export function boardEventSubject(ev: BoardEvent): string {
	switch (ev.type) {
		case EventTypeSaveDetected:
		case EventTypeSaveParsing:
		case EventTypeSaveRetry:
			return ev.data.name;
		case EventTypeSaveError:
			return ev.data.detail;
		case EventTypeSnapshotReady:
			return ev.data.save.name;
		case EventTypeHealthLeg:
			return ev.data.leg;
		case EventTypePlaythroughChanged:
			return ev.data.game_guid;
		case EventTypeResync:
			// Nothing to read: the absence of a payload is the payload.
			return ev.data ?? 'resync';
		default: {
			const unhandled: never = ev;
			return unhandled;
		}
	}
}
