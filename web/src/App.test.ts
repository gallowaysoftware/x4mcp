/**
 * The whole board, server-rendered once.
 *
 * This is a smoke test with one job: prove the tree assembles. `npm run build`
 * only proves every file compiles; it cannot catch a panel handed a prop that
 * no longer exists, or a store getter that throws the first time something
 * reads it. Rendering the real `App` against the real store does.
 *
 * What it asserts beyond "it did not throw" is the resting state itself, which
 * is the one product claim this step exists to make: before any save has been
 * parsed the board shows no numbers, and it says so.
 */
import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';

import App from './App.svelte';

const body = render(App).body;

describe('the board at rest', () => {
	it('assembles every slot', () => {
		for (const label of ['vitals', 'alert lane', 'idle ships and why', 'station health', 'threats', 'advisor']) {
			expect(body).toContain(`aria-label="${label}"`);
		}
	});

	it('is gray — a glance that lands on gray IS the all-clear', () => {
		expect(body).toContain('data-sev="gray"');
	});

	it('invents no numbers before a save has been parsed', () => {
		// design §6 startup: "no fake data". Every count is the dotted ∅ box, not
		// a zero, because "0 ships" is a claim nobody made.
		expect(body).toContain('waiting for first save');
		expect(body).toContain('∅ credits');
		expect(body).toContain('∅ fleet');
		expect(body).toContain('∅ threats');
	});

	it('opens with the arming checklist, because the alarm is not armed yet', () => {
		expect(body).toContain('first run — arming');
		expect(body).toContain('enable + test');
	});

	it('confesses the silent chime in vitals, permanently', () => {
		// design §7: a silently disarmed alarm is the worst honesty failure this
		// product can have.
		expect(body).toContain('MUTED');
	});

	it('is never blank — the lane proves it is alive instead', () => {
		expect(body).toContain('no alerts');
	});
});
