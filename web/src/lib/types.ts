// Types miroirs des DTO Go (internal/api, internal/store, internal/engine).

export type Role = 'admin' | 'operator' | 'viewer';
export type Permission = '' | 'view' | 'run' | 'edit';
export type EngineKind = 'rsync' | 'rclone';
export type Mode = 'copy' | 'sync' | 'move' | 'bisync';
export type EndpointType = 'local' | 'remote' | 'ssh';
export type RunState =
	| 'queued'
	| 'starting'
	| 'running'
	| 'cancelling'
	| 'succeeded'
	| 'partial'
	| 'failed'
	| 'cancelled'
	| 'interrupted'
	| 'skipped';

export const ACTIVE_STATES: RunState[] = ['queued', 'starting', 'running', 'cancelling'];

export interface User {
	id: number;
	username: string;
	role: Role;
	mustChangePassword: boolean;
	disabled: boolean;
	createdAt: string;
	lastLoginAt?: string;
}

export interface Me {
	user: User;
	csrfToken: string;
	oidcEnabled: boolean;
}

export interface SSHTarget {
	host: string;
	port: number;
	user: string;
	identity: { managed?: number; mounted?: boolean };
}

export interface Endpoint {
	type: EndpointType;
	path: string;
	contentsOnly?: boolean;
	remote?: string;
	ssh?: SSHTarget;
}

export interface Progress {
	phase: 'scanning' | 'transferring' | 'finalizing' | '';
	bytesDone: number;
	bytesTotal: number;
	filesDone: number;
	filesTotal: number;
	totalsExact: boolean;
	deletes: number;
	errors: number;
	speedBps: number;
	etaSec?: number;
	current?: { name: string; size: number; percent: number }[];
}

export interface Run {
	id: string;
	jobId?: number;
	jobName: string;
	engine: EngineKind;
	trigger: 'manual' | 'schedule' | 'retry';
	triggeredBy?: number;
	dryRun: boolean;
	attempt: number;
	state: RunState;
	queuedAt: string;
	startedAt?: string;
	finishedAt?: string;
	exitCode?: number;
	error?: string;
	bytesDone: number;
	bytesTotal?: number;
	filesDone: number;
	filesTotal?: number;
	filesDeleted: number;
	errors: number;
	logSize: number;
	live?: { state: RunState; progress: Progress };
	permission: Permission;
}

export interface Job {
	id: number;
	name: string;
	engine: EngineKind;
	mode: Mode;
	source: Endpoint;
	destination: Endpoint;
	options: Record<string, unknown>;
	scheduleCron?: string;
	scheduleEnabled: boolean;
	retryInterrupted: boolean;
	retryMax: number;
	retryDelaySec: number;
	catchUp: boolean;
	ownerId?: number;
	createdAt: string;
	updatedAt: string;
	permission: Permission;
	nextRunAt?: string;
	retryAt?: string;
	lastRun?: Run;
	active: boolean;
}

export interface JobInput {
	name: string;
	engine: EngineKind;
	mode: Mode;
	source: Endpoint;
	destination: Endpoint;
	options: Record<string, unknown>;
	schedule: { cron: string; enabled: boolean } | null;
	retryInterrupted: boolean;
	retryMax: number;
	retryDelaySec: number;
	catchUp: boolean;
}

export interface LogRecord {
	t: number;
	s: string;
	lvl: 'debug' | 'info' | 'warn' | 'error';
	msg: string;
}

export interface Item {
	t: string;
	op: 'transfer' | 'create' | 'delete' | 'attrs' | 'other';
	kind?: string;
	path: string;
	size: number;
	flags?: string;
}

export interface SystemInfo {
	version: string;
	rcloneVersion?: string;
	rsyncVersion?: string;
	allowedRoots: string[];
	maxConcurrent: number;
	rcloneEnabled: boolean;
	sshAvailable: boolean;
	sshMountedAvailable: boolean;
	oidcEnabled: boolean;
}

export interface SSHIdentityInfo {
	id: number;
	name: string;
	keyType: string;
	publicKey: string;
	fingerprint: string;
	createdAt: string;
}

export interface KnownHost {
	id: number;
	host: string;
	port: number;
	keyType: string;
	publicKey: string;
	fingerprint: string;
	createdAt: string;
}

export interface ScannedKey {
	type: string;
	fingerprint: string;
	key: string;
}

export interface Settings {
	maxConcurrent: number;
	rsyncVanishedIsSuccess: boolean;
	logRetentionDays: number;
}

export type NotifyMode = 'never' | 'failures' | 'all';

/** Configuration renvoyée par l'API : URL masquées, jeton jamais renvoyé. */
export interface NotificationSettings {
	on: NotifyMode;
	includeDryRuns: boolean;
	webhookUrl: string;
	webhookSet: boolean;
	ntfyUrl: string;
	ntfySet: boolean;
	ntfyTokenSet: boolean;
}

/** Mise à jour : un champ secret absent conserve la valeur, "" l'efface. */
export interface NotificationInput {
	on: NotifyMode;
	includeDryRuns: boolean;
	webhookUrl?: string;
	ntfyUrl?: string;
	ntfyToken?: string;
}

export interface BrowseEntry {
	name: string;
	dir: boolean;
	size: number;
	modTime: string;
	symlink?: boolean;
}

export interface RemoteSummary {
	name: string;
	type: string;
}

export interface ProviderOption {
	name: string;
	help: string;
	type: string;
	default?: unknown;
	required: boolean;
	advanced: boolean;
	exclusive: boolean;
	secret: boolean;
	provider?: string;
	examples?: { value: string; help: string; provider?: string }[];
}

export interface Provider {
	type: string;
	description: string;
	options: ProviderOption[];
}

export interface ConfigResult {
	done: boolean;
	state?: string;
	question?: Record<string, unknown>;
	error?: string;
}

export interface StateEvent {
	runId: string;
	jobId: number;
	jobName: string;
	from?: RunState;
	to: RunState;
	dryRun: boolean;
	at: string;
	exitCode?: number;
	error?: string;
	progress?: Progress;
}

export interface ProgressEvent extends Progress {
	runId: string;
	jobId: number;
	state: RunState;
	percent: number;
}
