import type { DesktopShellState, ShellWidget } from "../../shared/types.js";

export class ClockWidget extends HTMLElement implements ShellWidget {
    readonly id = "clock";
    readonly layer = 10;
    private timer: number | null = null;

    connectedCallback(): void {
        this.classList.add("clock-widget");
        this.start();
    }

    disconnectedCallback(): void {
        this.stop();
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
        this.start();
    }

    unmount(): void {
        this.remove();
        this.stop();
    }

    update(_state: DesktopShellState): void {
        this.render();
    }

    private start(): void {
        if (this.timer !== null) {
            return;
        }
        this.render();
        this.timer = window.setInterval(() => this.render(), 30_000);
    }

    private stop(): void {
        if (this.timer === null) {
            return;
        }
        window.clearInterval(this.timer);
        this.timer = null;
    }

    private render(): void {
        const now = new Date();
        this.textContent = now.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
        this.setAttribute("datetime", now.toISOString());
        this.title = now.toLocaleDateString([], { weekday: "long", year: "numeric", month: "long", day: "numeric" });
    }
}

if (!customElements.get("agora-clock")) {
    customElements.define("agora-clock", ClockWidget);
}
