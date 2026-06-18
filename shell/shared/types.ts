export interface SurfaceGeometry {
    x: number;
    y: number;
    width: number;
    height: number;
}

export interface SurfaceEvent {
    id: string;
    title?: string;
    app_id?: string;
    role?: string;
    geometry?: SurfaceGeometry;
    focused?: boolean;
}

export type AgentStatus = "running" | "exited" | "stopped" | "available" | "busy" | "offline" | string;

export interface AgentInfo {
    identity: string;
    name?: string;
    uid?: number;
    status: AgentStatus;
    last_seen?: string;
}

export interface BusSender {
    uid?: number;
    kind?: string;
    identity?: string;
}

export interface BusEnvelope<TBody = unknown> {
    topic: string;
    body?: TBody;
    sender?: BusSender;
    timestamp?: string;
}

export interface ShellNotification {
    id: string;
    title?: string;
    message: string;
    level?: "info" | "success" | "warning" | "error" | string;
    timestamp?: string;
    topic?: string;
}

export interface DesktopShellConfig {
    theme?: {
        background?: string;
        accent?: string;
        [key: string]: unknown;
    };
    [key: string]: unknown;
}

export interface DesktopShellState {
    surfaces: SurfaceEvent[];
    agents: AgentInfo[];
    notifications: ShellNotification[];
    config: DesktopShellConfig;
}

export interface ShellWidget {
    readonly id: string;
    readonly layer: number;
    mount(container: HTMLElement): void;
    unmount(): void;
    update(state: DesktopShellState): void;
}
