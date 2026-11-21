import { Stream } from './sse.ts';
import { get } from './api.ts';
import { ACTIVE_STATES, type ProgressEvent, type Run, type RunState, type StateEvent } from './types.ts';

export interface LiveRun {
	runId: string;
	jobId: number;
	jobName: string;
	state: RunState;
	dryRun: boolean;
	progress?: ProgressEvent;
	error?: string;
	at: string;
}

// État global alimenté par /api/v1/events : runs actifs et derniers changements.
export const live = $state<{ runs: Record<string, LiveRun>; connected: boolean; version: number }>({
	runs: {},
	connected: false,
	version: 0
});

let stream: Stream | null = null;

async function resync() {
	try {
		const res = await get<{ items: Run[] }>('/runs?state=' + ACTIVE_STATES.join(','));
		const runs: Record<string, LiveRun> = {};
		for (const r of res.items) {
			runs[r.id] = {
				runId: r.id,
				jobId: r.jobId ?? 0,
				jobName: r.jobName,
				state: r.live?.state ?? r.state,
				dryRun: r.dryRun,
				at: r.queuedAt
			};
		}
		live.runs = runs;
		live.version++;
	} catch {
		/* réessayé au prochain événement */
	}
}

export function startLive(onAuth: () => void) {
	if (stream) return;
	stream = new Stream('/api/v1/events', {
		onEvent(type, data) {
			if (type === 'run.state') {
				const ev = data as StateEvent;
				const prev = live.runs[ev.runId];
				live.runs[ev.runId] = {
					...(prev ?? {}),
					runId: ev.runId,
					jobId: ev.jobId,
					jobName: ev.jobName,
					state: ev.to,
					dryRun: ev.dryRun,
					error: ev.error,
					at: ev.at
				};
				live.version++;
			} else if (type === 'run.progress') {
				const ev = data as ProgressEvent;
				const prev = live.runs[ev.runId];
				if (prev) prev.progress = ev;
			}
		},
		onResync: resync,
		onAuth,
		onStatus(c) {
			live.connected = c;
		}
	});
	stream.open();
	void resync();
}

export function stopLive() {
	stream?.close();
	stream = null;
	live.runs = {};
}

export function activeRuns(): LiveRun[] {
	return Object.values(live.runs)
		.filter((r) => ACTIVE_STATES.includes(r.state))
		.sort((a, b) => a.runId.localeCompare(b.runId));
}

export function recentFinished(n = 8): LiveRun[] {
	return Object.values(live.runs)
		.filter((r) => !ACTIVE_STATES.includes(r.state))
		.sort((a, b) => b.at.localeCompare(a.at))
		.slice(0, n);
}
