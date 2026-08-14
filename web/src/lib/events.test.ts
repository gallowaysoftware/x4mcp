import { describe, expect, it } from 'vitest';

import { parseEnvelope } from './events';
import type { EventType } from './types.gen';

function frame(data: string): MessageEvent<string> {
	return { data } as MessageEvent<string>;
}

const BODY = { seq: 7, type: 'save.parsing', at: '2026-08-12T21:58:00Z', data: { name: 'quicksave' } };

describe('parseEnvelope — the trust boundary', () => {
	it('accepts a well-formed envelope', () => {
		const out = parseEnvelope('save.parsing', frame(JSON.stringify(BODY)));
		expect(out?.seq).toBe(7);
	});

	it('drops a frame that is not JSON rather than throwing into the reducer', () => {
		expect(parseEnvelope('save.parsing', frame('not json'))).toBeUndefined();
	});

	it('drops a frame whose body disagrees with the event name it arrived under', () => {
		// The listener is per name; a body claiming to be something else means
		// the payload type the reducer will assume is wrong.
		expect(parseEnvelope('snapshot.ready', frame(JSON.stringify(BODY)))).toBeUndefined();
	});

	it('drops an envelope with no usable sequence', () => {
		expect(parseEnvelope('save.parsing', frame(JSON.stringify({ ...BODY, seq: '7' })))).toBeUndefined();
		expect(parseEnvelope('save.parsing', frame(JSON.stringify({ ...BODY, at: undefined })))).toBeUndefined();
	});

	it('drops a bare JSON scalar', () => {
		for (const raw of ['null', '"resync"', '42', '[]']) {
			expect(parseEnvelope('resync' as EventType, frame(raw))).toBeUndefined();
		}
	});

	it('accepts a resync, which legitimately carries no data at all', () => {
		const out = parseEnvelope('resync', frame(JSON.stringify({ seq: 9, type: 'resync', at: BODY.at })));
		expect(out?.type).toBe('resync');
	});
});
