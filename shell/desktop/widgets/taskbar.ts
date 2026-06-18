import type { DesktopShellState, ShellWidget, SurfaceEvent } from "../../shared/types.js";

export type ShellPublisher = (topic: string, body: unknown) => void;

export class TaskbarWidget extends HTMLElement implements ShellWidget {
    readonly id = "taskbar";
    readonly layer = 30;
    private publish: ShellPublisher;
    private surfaces: SurfaceEvent[] = [];

    constructor(publish: ShellPublisher = () => undefined) {
        super();
        this.publish = publish;
    }

    connectedCallback(): void {
        this.classList.add("taskbar-widget");
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
        this.render();
    }

    unmount(): void {
        this.remove();
    }

    update(state: DesktopShellState): void {
        this.surfaces = [...state.surfaces].sort((a, b) => surfaceLabel(a).localeCompare(surfaceLabel(b)));
        this.render();
    }

    private render(): void {
        this.replaceChildren();
        const launch = button("taskbar-widget__launch", "⌘", "Open launcher");
        launch.addEventListener("click", () => this.publish("conversation.turn.requested", { prompt: "Open launcher" }));
        const surfaceList = document.createElement("div");
        surfaceList.className = "taskbar-widget__surfaces";
        for (const surface of this.surfaces) {
            const icon = button(
                `taskbar-widget__surface${surface.focused ? " taskbar-widget__surface--focused" : ""}`,
                surfaceIcon(surface),
                `Highlight ${surfaceLabel(surface)}`,
            );
            icon.title = surfaceLabel(surface);
            icon.dataset.surfaceId = surface.id;
            icon.addEventListener("click", () => this.publish("compositor.advisory.surface.highlight_requested", { surface_id: surface.id }));
            surfaceList.append(icon);
        }
        const indicator = document.createElement("span");
        indicator.className = "taskbar-widget__session";
        indicator.textContent = `${this.surfaces.length} surfaces · Agora`;
        this.append(launch, surfaceList, indicator);
    }
}

function button(className: string, text: string, label: string): HTMLButtonElement {
    const node = document.createElement("button");
    node.type = "button";
    node.className = className;
    node.textContent = text;
    node.setAttribute("aria-label", label);
    return node;
}

function surfaceLabel(surface: SurfaceEvent): string {
    return surface.title || surface.app_id || surface.id;
}

function surfaceIcon(surface: SurfaceEvent): string {
    const label = surfaceLabel(surface).trim();
    return (label[0] ?? "•").toUpperCase();
}

if (!customElements.get("agora-taskbar")) {
    customElements.define("agora-taskbar", TaskbarWidget);
}
