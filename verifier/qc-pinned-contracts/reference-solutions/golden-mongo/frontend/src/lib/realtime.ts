import type {
  ConnectionState,
  RealtimeEvent,
  Session,
} from "../types";

const EVENT_CURSOR_KEY = "labflow.last-event.v1";

interface RealtimeOptions {
  session: Session;
  onEvent: (event: RealtimeEvent) => void;
  onPoll: () => Promise<void>;
  onState: (state: ConnectionState) => void;
}

function websocketUrl(lastEventId: string): string {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  const query = lastEventId
    ? `?since=${encodeURIComponent(lastEventId)}`
    : "";
  return `${scheme}//${window.location.host}/api/events${query}`;
}

function bearerProtocol(token: string): string {
  const bytes = new TextEncoder().encode(token);
  let binary = "";
  bytes.forEach((value) => {
    binary += String.fromCharCode(value);
  });
  return `bearer.${btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replaceAll("=", "")}`;
}

/**
 * Maintains an ordered WebSocket stream and falls back to polling while
 * reconnecting. Event cursors survive navigation within the current tab.
 */
export class RealtimeClient {
  private socket: WebSocket | null = null;
  private reconnectTimer: number | null = null;
  private pollTimer: number | null = null;
  private watchdogTimer: number | null = null;
  private stopped = false;
  private failures = 0;
  private lastMessageAt = 0;

  constructor(private readonly options: RealtimeOptions) {}

  /** Starts the realtime connection and browser network listeners. */
  start(): void {
    this.stopped = false;
    window.addEventListener("online", this.handleOnline);
    window.addEventListener("offline", this.handleOffline);
    document.addEventListener("visibilitychange", this.handleVisibility);
    this.connect();
  }

  /** Stops sockets, retries, polling, and browser listeners. */
  stop(): void {
    this.stopped = true;
    this.clearTimers();
    this.socket?.close(1000, "client stopped");
    this.socket = null;
    window.removeEventListener("online", this.handleOnline);
    window.removeEventListener("offline", this.handleOffline);
    document.removeEventListener("visibilitychange", this.handleVisibility);
  }

  /** Forces a clean reconnect without losing the last received event cursor. */
  reconnect(): void {
    if (this.stopped) return;
    this.socket?.close(4000, "client reconnect");
    this.socket = null;
    this.scheduleReconnect(0);
  }

  private readonly handleOnline = (): void => {
    this.failures = 0;
    this.scheduleReconnect(0);
  };

  private readonly handleOffline = (): void => {
    this.options.onState("offline");
    this.socket?.close();
  };

  private readonly handleVisibility = (): void => {
    if (document.visibilityState === "visible" && !this.socket) {
      this.scheduleReconnect(0);
    }
  };

  private connect(): void {
    if (this.stopped || !navigator.onLine) {
      this.options.onState("offline");
      return;
    }
    this.options.onState("connecting");
    const cursor = sessionStorage.getItem(EVENT_CURSOR_KEY) ?? "";
    try {
      const socket = new WebSocket(websocketUrl(cursor), [
        "lab-logistics-v1",
        bearerProtocol(this.options.session.token),
      ]);
      this.socket = socket;
      socket.onopen = () => {
        if (this.socket !== socket) return;
        this.failures = 0;
        this.lastMessageAt = Date.now();
        this.stopPolling();
        this.options.onState("live");
        this.startWatchdog();
      };
      socket.onmessage = (message) => {
        this.lastMessageAt = Date.now();
        try {
          const event = JSON.parse(String(message.data)) as RealtimeEvent;
          const eventId = String(event.id ?? event.eventId ?? "");
          if (eventId) sessionStorage.setItem(EVENT_CURSOR_KEY, eventId);
          this.options.onEvent(event);
        } catch {
          // Ignore heartbeats and malformed messages without disrupting the stream.
        }
      };
      socket.onerror = () => socket.close();
      socket.onclose = () => {
        if (this.socket === socket) this.socket = null;
        this.stopWatchdog();
        if (this.stopped) return;
        this.failures += 1;
        if (this.failures >= 2) this.startPolling();
        this.scheduleReconnect();
      };
    } catch {
      this.failures += 1;
      this.startPolling();
      this.scheduleReconnect();
    }
  }

  private scheduleReconnect(delay?: number): void {
    if (this.stopped || this.reconnectTimer !== null) return;
    const exponential = Math.min(30_000, 1_000 * 2 ** this.failures);
    const jitter = Math.floor(Math.random() * 400);
    this.reconnectTimer = window.setTimeout(
      () => {
        this.reconnectTimer = null;
        this.connect();
      },
      delay ?? exponential + jitter,
    );
  }

  private startPolling(): void {
    if (this.pollTimer !== null || this.stopped || !navigator.onLine) return;
    this.options.onState("polling");
    const poll = () => {
      void this.options.onPoll().catch(() => {
        if (!navigator.onLine) this.options.onState("offline");
      });
    };
    poll();
    this.pollTimer = window.setInterval(poll, 7_500);
  }

  private stopPolling(): void {
    if (this.pollTimer !== null) window.clearInterval(this.pollTimer);
    this.pollTimer = null;
  }

  private startWatchdog(): void {
    this.stopWatchdog();
    this.watchdogTimer = window.setInterval(() => {
      if (Date.now() - this.lastMessageAt > 45_000) {
        this.socket?.close(4001, "heartbeat timeout");
      }
    }, 15_000);
  }

  private stopWatchdog(): void {
    if (this.watchdogTimer !== null) window.clearInterval(this.watchdogTimer);
    this.watchdogTimer = null;
  }

  private clearTimers(): void {
    if (this.reconnectTimer !== null) window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.stopPolling();
    this.stopWatchdog();
  }
}
