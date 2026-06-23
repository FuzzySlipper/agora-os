const SAFE_VISUAL_ID = /[^a-zA-Z0-9_-]+/g;

export function visualID(prefix: string, raw: string): string {
    const sanitized = raw.replace(SAFE_VISUAL_ID, "_").replace(/^_+|_+$/g, "").slice(0, 80);
    return `${prefix}_${sanitized || "unknown"}`;
}

export function applyVisualMarker(element: HTMLElement, id: string, role: string): void {
    element.dataset.visualId = id;
    element.dataset.visualRole = role;
    element.dataset.testid = id;
    element.setAttribute("data-visual-id", id);
    element.setAttribute("data-visual-role", role);
    element.setAttribute("data-testid", id);
}
