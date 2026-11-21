import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest';
import { Stream } from './sse.ts';

// Faux EventSource contrôlable.
class FakeES {
	static CONNECTING = 0;
	static OPEN = 1;
	static CLOSED = 2;
	static last: FakeES | null = null;
	static urls: string[] = [];
	readyState = FakeES.CONNECTING;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
	constructor(public url: string) {
		FakeES.last = this;
		FakeES.urls.push(url);
	}
	addEventListener(t: string, fn: (e: MessageEvent) => void) {
		(this.listeners[t] ??= []).push(fn);
	}
	close() {
		this.readyState = FakeES.CLOSED;
	}
	emit(t: string, data: unknown, id = '') {
		for (const fn of this.listeners[t] ?? []) fn({ data: JSON.stringify(data), lastEventId: id } as MessageEvent);
	}
}

const make = (u: string) => new FakeES(u) as unknown as EventSource;

describe('Stream', () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.stubGlobal('EventSource', FakeES);
		FakeES.urls = [];
	});
	afterEach(() => {
		vi.useRealTimers();
		vi.unstubAllGlobals();
	});

	it('backoff exponentiel plafonné', () => {
		expect(Stream.backoff(0)).toBe(1000);
		expect(Stream.backoff(3)).toBe(8000);
		expect(Stream.backoff(10)).toBe(30000);
	});

	it('relaie les événements et reprend avec le dernier id après fermeture', () => {
		const got: [string, unknown][] = [];
		const s = new Stream('/api/v1/events', { onEvent: (t, d) => got.push([t, d]) }, make);
		s.open();
		const es = FakeES.last!;
		es.onopen?.();
		es.emit('run.state', { runId: 'R', to: 'running' }, 'g:5');
		expect(got).toEqual([['run.state', { runId: 'R', to: 'running' }]]);
		es.readyState = FakeES.CLOSED;
		es.onerror?.();
		vi.advanceTimersByTime(1000);
		expect(FakeES.urls.at(-1)).toBe('/api/v1/events?lastEventId=g%3A5');
		s.close();
	});

	it('resync prévient ; auth ferme le flux sans reconnexion', () => {
		const onResync = vi.fn();
		const onAuth = vi.fn();
		const s = new Stream('/x', { onEvent: () => {}, onResync, onAuth }, make);
		s.open();
		const es = FakeES.last!;
		es.emit('resync', {});
		expect(onResync).toHaveBeenCalled();
		es.emit('auth', {});
		expect(onAuth).toHaveBeenCalled();
		expect(es.readyState).toBe(FakeES.CLOSED);
		vi.advanceTimersByTime(60_000);
		expect(FakeES.urls.length).toBe(1);
	});
});
