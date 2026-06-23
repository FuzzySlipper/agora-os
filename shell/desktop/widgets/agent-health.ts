import type { AgentInfo, DesktopShellState, ShellWidget } from "../../shared/types.js";
import { applyVisualMarker, visualID } from "../visual-markers.js";

export class AgentHealthWidget extends HTMLElement implements ShellWidget {
    readonly id = "agent-health";
    readonly layer = 10;

    connectedCallback(): void {
        this.classList.add("agent-health-widget");
        applyVisualMarker(this, "agent_health", "agent_health");
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
    }

    unmount(): void {
        this.remove();
    }

    update(state: DesktopShellState): void {
        const agents = [...state.agents].sort((a, b) => agentLabel(a).localeCompare(agentLabel(b)));
        this.replaceChildren();
        this.append(el("span", "agent-health-widget__summary", `${agents.length} agents`));
        const dots = el("span", "agent-health-widget__dots");
        for (const agent of agents) {
            const dot = el("span", `agent-health-widget__dot agent-health-widget__dot--${statusClass(agent.status)}`);
            applyVisualMarker(dot, visualID("agent_status", agentLabel(agent)), "status_indicator");
            dot.title = `${agentLabel(agent)}: ${agent.status}`;
            dot.setAttribute("aria-label", dot.title);
            dots.append(dot);
        }
        this.append(dots);
    }
}

function agentLabel(agent: AgentInfo): string {
    return agent.identity || "agent";
}

function statusClass(status: string): "active" | "idle" | "offline" {
    const normalized = status.toLowerCase();
    if (["running", "active", "busy", "available"].includes(normalized)) {
        return "active";
    }
    if (["idle", "stopped", "exited"].includes(normalized)) {
        return "idle";
    }
    return "offline";
}

function el(tag: string, className: string, text = ""): HTMLElement {
    const node = document.createElement(tag);
    node.className = className;
    node.textContent = text;
    return node;
}

if (!customElements.get("agora-agent-health")) {
    customElements.define("agora-agent-health", AgentHealthWidget);
}
