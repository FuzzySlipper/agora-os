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
        const chips = el("span", "agent-health-widget__chips");
        for (const agent of agents) {
            const normalizedStatus = statusClass(agent.status);
            const chip = el("span", `agent-health-widget__chip agent-health-widget__chip--${normalizedStatus}`);
            applyVisualMarker(chip, visualID("agent_status", agentLabel(agent)), "status_indicator");
            chip.title = `${agentLabel(agent)}: ${agent.status}`;
            chip.setAttribute("aria-label", chip.title);
            chip.append(el("span", "agent-health-widget__dot"), el("span", "agent-health-widget__label", `${agentLabel(agent)} ${agent.status}`));
            chips.append(chip);
        }
        this.append(chips);
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
