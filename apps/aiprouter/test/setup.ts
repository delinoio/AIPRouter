// SPDX-License-Identifier: Apache-2.0
import { afterEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";
afterEach(() => cleanup());
// jsdom has no top-layer rendering. Browser acceptance must verify native focus trapping.
if (!HTMLDialogElement.prototype.showModal)
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
if (!HTMLDialogElement.prototype.close)
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
});
