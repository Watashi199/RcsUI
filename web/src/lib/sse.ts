// Connexion SSE avec reconnexion : EventSource renvoie Last-Event-ID tout seul ;
// on ajoute un backoff exponentiel quand la connexion est fermée par le serveur
// (session expirée, abonné trop lent, redémarrage) et la gestion de "resync".

export interface StreamHandlers {
	onEvent: (type: string, data: unknown) => void;
	/** Appelé quand l'historique n'est plus disponible : recharger l'état via REST. */
	onResync?: () => void;
	/** Appelé quand la session a expiré. */
	onAuth?: () => void;
	onStatus?: (connected: boolean) => void;
}

export const EVENT_TYPES = ['run.state', 'run.progress', 'run.log', 'run.item'];

export class Stream {
	private es: EventSource | null = null;
	private lastId = '';
	private attempt = 0;
	private timer: ReturnType<typeof setTimeout> | null = null;
	private closed = false;

	constructor(
		private url: string,
		private h: StreamHandlers,
		private makeSource: (url: string) => EventSource = (u) => new EventSource(u, { withCredentials: true })
	) {}

	open() {
		this.closed = false;
		this.connect();
	}

	close() {
		this.closed = true;
		if (this.timer) clearTimeout(this.timer);
		this.es?.close();
		this.es = null;
		this.h.onStatus?.(false);
	}

	/** Délai de reconnexion : 1 s, 2 s, 4 s… plafonné à 30 s. */
	static backoff(attempt: number): number {
		return Math.min(30_000, 1000 * 2 ** Math.max(0, attempt));
	}

	private connect() {
		if (this.closed) return;
		const sep = this.url.includes('?') ? '&' : '?';
		// Après une fermeture explicite, EventSource ne renvoie plus Last-Event-ID : on le passe en paramètre.
		const url = this.lastId ? `${this.url}${sep}lastEventId=${encodeURIComponent(this.lastId)}` : this.url;
		const es = this.makeSource(url);
		this.es = es;
		es.onopen = () => {
			this.attempt = 0;
			this.h.onStatus?.(true);
		};
		const handle = (type: string) => (ev: MessageEvent) => {
			if (ev.lastEventId) this.lastId = ev.lastEventId;
			let data: unknown = null;
			try {
				data = JSON.parse(ev.data);
			} catch {
				return;
			}
			this.h.onEvent(type, data);
		};
		for (const t of EVENT_TYPES) es.addEventListener(t, handle(t) as EventListener);
		es.addEventListener('resync', (() => {
			this.lastId = '';
			this.h.onResync?.();
		}) as EventListener);
		es.addEventListener('auth', (() => {
			this.close();
			this.h.onAuth?.();
		}) as EventListener);
		es.onerror = () => {
			this.h.onStatus?.(false);
			if (es.readyState === EventSource.CLOSED) {
				es.close();
				if (this.closed) return;
				this.timer = setTimeout(() => this.connect(), Stream.backoff(this.attempt++));
			}
			// readyState CONNECTING : le navigateur se reconnecte seul (retry: 3000).
		};
	}
}
