import type { BusEnvelope } from "./types";

export type BusHandler<TBody = unknown> = (envelope: BusEnvelope<TBody>) => void;
export type BusStatus = "disconnected" | "connecting" | "connected";

export interface BusConnectionOptions {
    url?: string | URL;
    protocols?: string | string[];
    WebSocketCtor?: typeof WebSocket;
    minReconnectDelayMs?: number;
    maxReconnectDelayMs?: number;
}

const DEFAULT_MIN_RECONNECT_DELAY_MS = 1000;
const DEFAULT_MAX_RECONNECT_DELAY_MS = 30000;

export class BusConnection {
    private readonly url: string;
    private readonly protocols?: string | string[];
    private readonly WebSocketCtor: typeof WebSocket;
    private readonly minReconnectDelayMs: number;
    private readonly maxReconnectDelayMs: number;
    private readonly handlers = new Map<string, Set<BusHandler>>();
    private socket: WebSocket | null = null;
    private reconnectTimer: ReturnType<typeof globalThis.setTimeout> | null = null;
    private reconnectDelayMs: number;
    private shouldReconnect = false;

    status: BusStatus = "disconnected";

    constructor(options: BusConnectionOptions = {}) {
        this.url = String(options.url ?? defaultBusURL());
        this.protocols = options.protocols;
        this.WebSocketCtor = options.WebSocketCtor ?? WebSocket;
        this.minReconnectDelayMs = options.minReconnectDelayMs ?? DEFAULT_MIN_RECONNECT_DELAY_MS;
        this.maxReconnectDelayMs = options.maxReconnectDelayMs ?? DEFAULT_MAX_RECONNECT_DELAY_MS;
        this.reconnectDelayMs = this.minReconnectDelayMs;
    }

    connect(): void {
        this.shouldReconnect = true;
        this.clearReconnectTimer();
        if (this.socket && (this.socket.readyState === this.WebSocketCtor.OPEN || this.socket.readyState === this.WebSocketCtor.CONNECTING)) {
            return;
        }
        this.status = "connecting";
        const socket = this.protocols
            ? new this.WebSocketCtor(this.url, this.protocols)
            : new this.WebSocketCtor(this.url);
        this.socket = socket;

        socket.addEventListener("open", () => {
            this.status = "connected";
            this.reconnectDelayMs = this.minReconnectDelayMs;
            for (const topic of this.handlers.keys()) {
                this.sendControl("sub", topic);
            }
        });
        socket.addEventListener("message", (event) => this.dispatch(event.data));
        socket.addEventListener("close", () => {
            if (this.socket === socket) {
                this.socket = null;
                this.status = "disconnected";
            }
            this.scheduleReconnect();
        });
        socket.addEventListener("error", () => {
            if (this.socket === socket) {
                socket.close();
            }
        });
    }

    disconnect(): void {
        this.shouldReconnect = false;
        this.clearReconnectTimer();
        const socket = this.socket;
        this.socket = null;
        this.status = "disconnected";
        if (socket && socket.readyState !== this.WebSocketCtor.CLOSED && socket.readyState !== this.WebSocketCtor.CLOSING) {
            socket.close();
        }
    }

    subscribe<TBody = unknown>(topic: string, handler: BusHandler<TBody>): void {
        let topicHandlers = this.handlers.get(topic);
        if (!topicHandlers) {
            topicHandlers = new Set();
            this.handlers.set(topic, topicHandlers);
            this.sendControl("sub", topic);
        }
        topicHandlers.add(handler as BusHandler);
    }

    unsubscribe(topic: string): void {
        if (!this.handlers.delete(topic)) {
            return;
        }
        this.sendControl("unsub", topic);
    }

    publish<TBody = unknown>(topic: string, body: TBody): void {
        this.send({ op: "pub", topic, body });
    }

    private sendControl(op: "sub" | "unsub", topic: string): void {
        this.send({ op, topic });
    }

    private send(payload: unknown): void {
        if (!this.socket || this.socket.readyState !== this.WebSocketCtor.OPEN) {
            return;
        }
        this.socket.send(JSON.stringify(payload));
    }

    private dispatch(rawData: unknown): void {
        const envelope = parseEnvelope(rawData);
        if (!envelope) {
            return;
        }
        for (const [topic, handlers] of this.handlers.entries()) {
            if (!topicMatches(topic, envelope.topic)) {
                continue;
            }
            for (const handler of handlers) {
                handler(envelope);
            }
        }
    }

    private scheduleReconnect(): void {
        if (!this.shouldReconnect || this.reconnectTimer !== null) {
            return;
        }
        const delay = this.reconnectDelayMs;
        this.reconnectDelayMs = Math.min(this.reconnectDelayMs * 2, this.maxReconnectDelayMs);
        this.reconnectTimer = globalThis.setTimeout(() => {
            this.reconnectTimer = null;
            this.connect();
        }, delay);
    }

    private clearReconnectTimer(): void {
        if (this.reconnectTimer === null) {
            return;
        }
        globalThis.clearTimeout(this.reconnectTimer);
        this.reconnectTimer = null;
    }
}

export function createBusConnection(options: BusConnectionOptions = {}): BusConnection {
    return new BusConnection(options);
}

export function parseEnvelope(rawData: unknown): BusEnvelope | null {
    const text = typeof rawData === "string" ? rawData : rawData instanceof Blob ? null : String(rawData ?? "");
    if (!text) {
        return null;
    }
    try {
        const parsed = JSON.parse(text) as BusEnvelope;
        if (parsed && typeof parsed.topic === "string") {
            return parsed;
        }
    } catch {
        return null;
    }
    return null;
}

export function topicMatches(subscription: string, topic: string): boolean {
    if (subscription === topic || subscription === "*") {
        return true;
    }
    if (!subscription.includes("*")) {
        return false;
    }
    const escaped = subscription
        .split("*")
        .map((part) => part.replace(/[|\\{}()[\]^$+?.]/g, "\\$&"))
        .join(".*");
    return new RegExp(`^${escaped}$`).test(topic);
}

function defaultBusURL(): string {
    if (!globalThis.location?.href) {
        throw new Error("BusConnection requires an explicit url when no browser location is available");
    }
    const url = new URL("/ws", globalThis.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    return url.toString();
}
