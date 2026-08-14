/**
 * The one chime (design §7).
 *
 * Two-note descending minor third, E5 → C♯5, mallet/bell timbre, ≤600 ms, soft
 * attack, default −18 dBFS. Reds only, once per parse batch. It is synthesised
 * rather than shipped as a file for one honest reason: an asset is a fetch that
 * can fail at exactly the moment the alarm is needed, and this cannot.
 *
 * The timbre brief is "distinct from X4's klaxons" — the game next door is
 * full of harsh synthetic alarms, so this is a struck bell: a fast soft attack,
 * an exponential decay, and a quiet inharmonic partial. Nothing about it should
 * sound like the game.
 */

/** Equal-tempered A440. Named, because "659.25" in a call site is a mystery. */
export const NOTE_E5 = 659.255;
export const NOTE_CS5 = 554.365;

/** design §7: default −18 dBFS. */
export const DEFAULT_DBFS = -18;

/** Amplitude for a dBFS level. −18 dBFS is ≈0.126 linear. */
export function gainFromDBFS(dbfs: number): number {
	return Math.pow(10, dbfs / 20);
}

export interface ChimeNote {
	frequency: number;
	/** Seconds from the start of the chime. */
	startS: number;
	/** Seconds until the note has decayed to silence. */
	durationS: number;
}

/**
 * The schedule, as data — pure so the ≤600 ms budget is a test and not a hope.
 * The second note overlaps the first's tail, which is what makes two strikes
 * read as one gesture instead of two beeps.
 */
export function chimeSchedule(): ChimeNote[] {
	return [
		{ frequency: NOTE_E5, startS: 0, durationS: 0.34 },
		{ frequency: NOTE_CS5, startS: 0.22, durationS: 0.38 },
	];
}

/** Total length of the chime in seconds; design §7 caps it at 0.6. */
export function chimeDurationS(notes: readonly ChimeNote[] = chimeSchedule()): number {
	return notes.reduce((end, n) => Math.max(end, n.startS + n.durationS), 0);
}

const ATTACK_S = 0.008;

type AudioContextCtor = new () => AudioContext;

function audioContextCtor(): AudioContextCtor | undefined {
	const w = globalThis as unknown as {
		AudioContext?: AudioContextCtor;
		webkitAudioContext?: AudioContextCtor;
	};
	return w.AudioContext ?? w.webkitAudioContext;
}

/**
 * Owns the single AudioContext and answers the only question that matters:
 * will a sound actually come out right now?
 *
 * Browsers start a context `suspended` until a user gesture, and `resume()`
 * from a non-gesture context resolves without doing anything on some engines.
 * So `unlocked` is read from the context's own state after every attempt, never
 * assumed from "we called resume".
 */
export class Chime {
	#ctx: AudioContext | undefined;
	#dbfs: number;

	constructor(dbfs = DEFAULT_DBFS) {
		this.#dbfs = dbfs;
	}

	/** False when this browser has no Web Audio at all — a real, reportable state. */
	get available(): boolean {
		return audioContextCtor() !== undefined;
	}

	get unlocked(): boolean {
		return this.#ctx !== undefined && this.#ctx.state === 'running';
	}

	/**
	 * Try to unlock audio. Called from a click handler this succeeds; called on
	 * load it usually does not, and returning false is the point — that false is
	 * what puts MUTED in vitals instead of a board that thinks it is armed.
	 */
	async unlock(): Promise<boolean> {
		const Ctor = audioContextCtor();
		if (Ctor === undefined) return false;
		try {
			// Constructing can throw too — some browsers cap the number of live
			// contexts. Every failure path here has the same honest answer:
			// false, which is what puts MUTED in vitals.
			this.#ctx ??= new Ctor();
			if (this.#ctx.state !== 'running') await this.#ctx.resume();
		} catch {
			return false;
		}
		return this.#ctx?.state === 'running';
	}

	/** Play once. Silent no-op when audio is not unlocked; the caller knows. */
	play(): void {
		const ctx = this.#ctx;
		if (ctx === undefined || ctx.state !== 'running') return;
		const peak = gainFromDBFS(this.#dbfs);
		const t0 = ctx.currentTime;
		for (const note of chimeSchedule()) {
			strike(ctx, note, t0 + note.startS, peak);
		}
	}

	/** Release the device. A board left open all evening should not hold it. */
	close(): void {
		void this.#ctx?.close();
		this.#ctx = undefined;
	}
}

function strike(ctx: AudioContext, note: ChimeNote, at: number, peak: number): void {
	const gain = ctx.createGain();
	gain.gain.setValueAtTime(0.0001, at);
	gain.gain.exponentialRampToValueAtTime(peak, at + ATTACK_S);
	gain.gain.exponentialRampToValueAtTime(0.0001, at + note.durationS);
	gain.connect(ctx.destination);

	const fundamental = ctx.createOscillator();
	fundamental.type = 'sine';
	fundamental.frequency.setValueAtTime(note.frequency, at);
	fundamental.connect(gain);

	// A quiet partial a little above the octave: enough inharmonicity to read
	// as struck metal rather than as a test tone, far too little to be a chord.
	const partial = ctx.createOscillator();
	partial.type = 'sine';
	partial.frequency.setValueAtTime(note.frequency * 2.76, at);
	const partialGain = ctx.createGain();
	partialGain.gain.setValueAtTime(0.18, at);
	partial.connect(partialGain);
	partialGain.connect(gain);

	fundamental.start(at);
	partial.start(at);
	fundamental.stop(at + note.durationS + 0.02);
	partial.stop(at + note.durationS + 0.02);
}
