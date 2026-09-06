// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  statuses: vi.fn(),
  connect: vi.fn(),
  logout: vi.fn(),
  openExternalURL: vi.fn(),
  getConfig: vi.fn(),
}));

vi.mock("@/api/subscriptions", () => ({
  getSubscriptionStatuses: mocks.statuses,
  connectSubscription: mocks.connect,
  logoutSubscription: mocks.logout,
  cancelSubscriptionLogin: vi.fn(),
}));
vi.mock("@/api/config", () => ({
  getConfig: mocks.getConfig,
  updateLLMConfig: vi.fn(),
}));
vi.mock("@/api/runtime", () => ({ openExternalURL: mocks.openExternalURL }));

import { SubscriptionProviders } from "./SubscriptionProviders";

let root: Root;
let container: HTMLDivElement;

beforeEach(() => {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  mocks.statuses.mockResolvedValue([
    {
      provider: "chatgpt",
      available: true,
      connected: false,
      connecting: false,
    },
    { provider: "kimi", available: true, connected: true, connecting: false },
  ]);
  mocks.getConfig.mockResolvedValue({ llm: { subscriptions: [] } });
  mocks.connect.mockResolvedValue({
    authorization_url: "https://auth.example",
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("SubscriptionProviders", () => {
  it("shows the ChatGPT notice and safe provider statuses when expanded", async () => {
    await act(async () => {
      root.render(<SubscriptionProviders isExpanded onToggle={vi.fn()} />);
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain(
      "ChatGPT personal-use compatibility (experimental)",
    );
    expect(document.body.textContent).toContain("Disconnected");
    expect(document.body.textContent).toContain("Connected");
    expect(document.body.textContent).toContain(
      "never imports browser cookies",
    );
  });

  it("opens only the returned authorization URL in the system browser", async () => {
    await act(async () => {
      root.render(<SubscriptionProviders isExpanded onToggle={vi.fn()} />);
      await Promise.resolve();
    });
    await act(async () => {
      (
        Array.from(document.querySelectorAll("button")).find(
          (button) => button.textContent === "Connect",
        ) as HTMLButtonElement
      ).click();
      await Promise.resolve();
    });

    expect(mocks.connect).toHaveBeenCalledWith("chatgpt");
    expect(mocks.openExternalURL).toHaveBeenCalledWith("https://auth.example");
  });
});
