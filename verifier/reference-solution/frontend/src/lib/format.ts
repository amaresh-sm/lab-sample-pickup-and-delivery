import type { Role } from "../types";
import { ROLE_LABELS } from "../types";

export function titleCase(value: unknown): string {
  return String(value ?? "")
    .replaceAll("_", " ")
    .replace(/\b\w/g, (character) => character.toUpperCase());
}

export function formatTime(value: unknown): string {
  if (!value) return "—";
  const parsed = new Date(String(value));
  if (Number.isNaN(parsed.valueOf())) return String(value);
  return new Intl.DateTimeFormat("en-IN", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(parsed);
}

export function displayId(value: unknown): string {
  const id = String(value ?? "");
  if (id.length <= 18) return id || "—";
  return `${id.slice(0, 9)}…${id.slice(-6)}`;
}

export function roleLabel(role: Role | null): string {
  return role ? ROLE_LABELS[role] : "No active role";
}

/**
 * Labels the role recorded on an audit event. Besides the seven roles, an audit
 * record may name the application's own timers or an actor who had not yet
 * selected a role.
 */
export function auditRoleLabel(value: string): string {
  if (value === "system") return "System timers";
  if (value === "unselected") return "No role selected";
  return value in ROLE_LABELS ? ROLE_LABELS[value as Role] : titleCase(value);
}

export function hasRole(
  roles: Role[],
  allowed: readonly Role[],
): boolean {
  return roles.length === 0 || allowed.some((role) => roles.includes(role));
}

export function localDateTime(offsetHours = 0): string {
  const date = new Date(Date.now() + offsetHours * 60 * 60 * 1000);
  date.setMinutes(date.getMinutes() - date.getTimezoneOffset());
  return date.toISOString().slice(0, 16);
}

export function strings(value: string): string[] {
  const trimmed = value.trim();
  if (!trimmed) return [];
  try {
    const parsed: unknown = JSON.parse(trimmed);
    if (Array.isArray(parsed)) return parsed.map(String);
  } catch {
    // Comma-separated input is intentionally supported.
  }
  return trimmed.split(",").map((item) => item.trim()).filter(Boolean);
}

export function objectJson(value: string): Record<string, unknown> {
  const parsed: unknown = JSON.parse(value || "{}");
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error("Enter a JSON object.");
  }
  return parsed as Record<string, unknown>;
}
