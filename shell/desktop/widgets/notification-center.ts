import type { DesktopShellState, ShellNotification, ShellWidget } from "../../shared/types.js";
import { applyVisualMarker, visualID } from "../visual-markers.js";

const MAX_VISIBLE = 5;
const DISMISS_AFTER_MS = 10_000;

export class NotificationCenter extends HTMLElement implements ShellWidget {
    readonly id = "notifications";
    readonly layer = 20;
    private timers = new Map<string, number>();
    private dismissed = new Set<string>();

    connectedCallback(): void {
        this.classList.add("notification-center");
        applyVisualMarker(this, "notification_stack", "notification_stack");
    }

    mount(container: HTMLElement): void {
        container.replaceChildren(this);
    }

    unmount(): void {
        for (const timer of this.timers.values()) {
            window.clearTimeout(timer);
        }
        this.timers.clear();
        this.remove();
    }

    update(state: DesktopShellState): void {
        const notifications = state.notifications
            .filter((notification) => !this.dismissed.has(notification.id))
            .slice(0, MAX_VISIBLE);
        this.replaceChildren(...notifications.map((notification) => this.renderNotification(notification)));
        for (const notification of notifications) {
            this.ensureDismissTimer(notification.id);
        }
    }

    private renderNotification(notification: ShellNotification): HTMLElement {
        const item = document.createElement("article");
        item.className = `notification-center__item notification-center__item--${notification.level ?? "info"}`;
        applyVisualMarker(item, visualID("notification", notification.id), "notification");
        item.dataset.notificationId = notification.id;
        const title = document.createElement("strong");
        title.className = "notification-center__title";
        const level = notification.level ?? "info";
        title.textContent = notification.title ?? notification.topic ?? "Notification";
        const severity = document.createElement("span");
        severity.className = "notification-center__severity";
        severity.textContent = level;
        const message = document.createElement("p");
        message.className = "notification-center__message";
        message.textContent = notification.message;
        item.append(title, severity, message);
        return item;
    }

    private ensureDismissTimer(id: string): void {
        if (this.timers.has(id)) {
            return;
        }
        const timer = window.setTimeout(() => {
            this.dismissed.add(id);
            this.timers.delete(id);
            this.querySelector(`[data-notification-id="${CSS.escape(id)}"]`)?.remove();
        }, DISMISS_AFTER_MS);
        this.timers.set(id, timer);
    }
}

if (!customElements.get("agora-notification-center")) {
    customElements.define("agora-notification-center", NotificationCenter);
}
