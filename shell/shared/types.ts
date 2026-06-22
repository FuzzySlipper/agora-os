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
    disabled?: boolean;
    status?: string;
    action_error?: string;
    always_on_top?: boolean;
    fullscreen?: boolean;
}

export type SurfaceActionDecision = "accepted" | "denied" | string;

export interface SurfaceState {
    always_on_top?: boolean;
    fullscreen?: boolean;
}

export interface SurfaceActionResponse {
    action: string;
    surface_id: string;
    decision: SurfaceActionDecision;
    reason?: string;
    error?: string;
    focused_surface_id?: string;
    closed_surface_id?: string;
    target_geometry?: SurfaceGeometry;
    result_geometry?: SurfaceGeometry;
    target_state?: SurfaceState;
    result_state?: SurfaceState;
    always_on_top?: boolean;
    fullscreen?: boolean;
    queued?: boolean;
    actor?: string;
    actor_uid?: number;
    surface?: {
        surface?: SurfaceEvent;
        focused?: boolean;
        visible?: boolean;
        [key: string]: unknown;
    };
}


export interface AppCatalogEntry {
    id: string;
    label: string;
    description?: string;
    icon?: string;
    tags?: string[];
    state: "ready" | "disabled" | "pending" | "error" | string;
    reason?: string;
}

export interface AppCatalogListResponse {
    entries: AppCatalogEntry[];
}

export interface AppLaunchActionResponse {
    action: "app.launch" | string;
    catalog_id: string;
    app_id?: string;
    decision: SurfaceActionDecision;
    reason?: string;
    error?: string;
    actor?: string;
    actor_uid?: number;
    launch_id?: string;
    pid?: number;
    surface?: {
        surface?: SurfaceEvent;
        focused?: boolean;
        visible?: boolean;
        [key: string]: unknown;
    };
    queued?: boolean;
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

export type CommandCenterActionState = "ready" | "pending" | "disabled" | "error";

export interface CommandCenterContext {
    source: "desktop-command-center";
    focused_surface_id?: string;
    visible_surface_ids: string[];
}

export interface ConversationTurnRequest {
    session_id: string;
    turn_id: string;
    prompt: string;
    context: CommandCenterContext;
}

export interface ConversationTranscriptEntry {
    turn_id: string;
    prompt?: string;
    response?: string;
    status: "pending" | "responded" | "error";
    timestamp?: string;
}

export interface CommandCenterState {
    open: boolean;
    query?: string;
    pendingTurnID?: string;
    error?: string;
    transcript: ConversationTranscriptEntry[];
}

export interface DesktopShellState {
    surfaces: SurfaceEvent[];
    agents: AgentInfo[];
    notifications: ShellNotification[];
    config: DesktopShellConfig;
    commandCenter: CommandCenterState;
}

export interface ShellWidget {
    readonly id: string;
    readonly layer: number;
    mount(container: HTMLElement): void;
    unmount(): void;
    update(state: DesktopShellState): void;
}
