/**
 * The typed fetch wrapper. Small on purpose (tech-design §3: ~100 lines) — the
 * server is same-origin, the payloads are generated types, and anything clever
 * here would be a second place for the wire contract to live.
 */

import type { StateView, VitalsView } from './types.gen';

/**
 * tech-design §2: `X-X4Cue: 1` is required on `/api/*` POSTs. It is a
 * CSRF/rebinding guard — a cross-origin form post cannot set a custom header
 * without a preflight the server never answers.
 */
const POST_HEADER = 'X-X4Cue';

export class ApiError extends Error {
	readonly status: number;
	constructor(status: number, message: string) {
		super(message);
		this.name = 'ApiError';
		this.status = status;
	}
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
	const res = await fetch(path, {
		method: 'GET',
		headers: { Accept: 'application/json' },
		cache: 'no-store',
		...(signal !== undefined ? { signal } : {}),
	});
	if (!res.ok) throw new ApiError(res.status, `GET ${path}: ${res.status} ${res.statusText}`);
	return (await res.json()) as T;
}

async function postJSON<T>(path: string): Promise<T> {
	const res = await fetch(path, {
		method: 'POST',
		headers: { Accept: 'application/json', [POST_HEADER]: '1' },
	});
	if (!res.ok) throw new ApiError(res.status, `POST ${path}: ${res.status} ${res.statusText}`);
	return (await res.json()) as T;
}

/** The bootstrap: everything a tab needs to draw itself from nothing. */
export function fetchState(signal?: AbortSignal): Promise<StateView> {
	return getJSON<StateView>('/api/state', signal);
}

/** The one mounted view in this build; panels fetch their own from S9 on. */
export function fetchVitals(signal?: AbortSignal): Promise<VitalsView> {
	return getJSON<VitalsView>('/api/views/vitals', signal);
}

/**
 * Ask the watcher to look now. Returns immediately — a parse is 5–16 s and the
 * stream, not this response, is what says what happened.
 */
export function refreshSave(): Promise<{ kicked: boolean; last_event_seq: number }> {
	return postJSON<{ kicked: boolean; last_event_seq: number }>('/api/admin/refresh-save');
}
