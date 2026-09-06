// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  statuses: vi.fn(),
  getConfig: vi.fn(),
  updateLLMConfig: vi.fn(),
}));

vi.mock("@/api/subscriptions", () => ({
  getSubscriptionStatuses: mocks.statuses,
  connectSubscription: vi.fn(),
  cancelSubscriptionLogin: vi.fn(),
  logoutSubscription: vi.fn(),
}));
vi.mock("@/api/config", () => ({
  getConfig: mocks.getConfig,
  updateLLMConfig: mocks.updateLLMConfig,
}));
vi.mock("@/api/runtime", () => ({ openExternalURL: vi.fn() }));

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
      connected: true,
      connecting: false,
    },
  ]);
  mocks.getConfig.mockResolvedValue({
    llm: {
      subscriptions: [
        {
          provider: "chatgpt_subscription",
          models: [
            "gpt-5.1-codex",
            "gpt-5.1-codex-max",
            "gpt-5.1-codex-mini",
            "gpt-5.3-chat-latest",
            "gpt-5.3-codex",
            "gpt-5.3-codex-spark",
            "gpt-5.4",
            "gpt-5.4-mini",
            "gpt-5.4-nano",
            "gpt-5.4-pro",
            "gpt-5.5",
            "gpt-5.5-pro",
            "gpt-5.6-luna",
            "gpt-5.6-sol",
            "gpt-5.6-terra",
          ],
          enabled: ["gpt-5.4"],
        },
      ],
    },
  });
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
});

describe("SubscriptionProviders model visibility", () => {
  it("is collapsed until the user opens it", async () => {
    await act(async () => {
      root.render(
        <SubscriptionProviders isExpanded={false} onToggle={vi.fn()} />,
      );
      await Promise.resolve();
    });

    const trigger = document.querySelector<HTMLButtonElement>(
      "button[aria-expanded]",
    );
    expect(trigger?.textContent).toContain("ChatGPT subscription");
    expect(trigger?.getAttribute("aria-expanded")).toBe("false");
    expect(document.body.textContent).not.toContain("Subscription models");
  });

  it("shows the subscription catalogue and persists the selected subset", async () => {
    await act(async () => {
      root.render(<SubscriptionProviders isExpanded onToggle={vi.fn()} />);
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("Subscription models");
    const boxes = Array.from(
      document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'),
    );
    expect(boxes).toHaveLength(15);
    const gpt54 = Array.from(document.querySelectorAll("label"))
      .find((label) => label.textContent === "gpt-5.4")
      ?.querySelector<HTMLInputElement>("input");
    const codexMax = Array.from(document.querySelectorAll("label"))
      .find((label) => label.textContent === "gpt-5.1-codex-max")
      ?.querySelector<HTMLInputElement>("input");
    expect(gpt54).toBeDefined();
    expect(codexMax).toBeDefined();
    expect(gpt54!.checked).toBe(true);
    expect(codexMax!.checked).toBe(false);

    await act(async () => {
      codexMax!.click();
      await Promise.resolve();
    });
    expect(mocks.updateLLMConfig).toHaveBeenCalledWith({
      subscription_models: {
        chatgpt_subscription: ["gpt-5.4", "gpt-5.1-codex-max"],
      },
    });
  });
});
