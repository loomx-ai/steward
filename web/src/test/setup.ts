import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";
import { loadMessages } from "@/i18n/messages";

// Production awaits the active locale before rendering; tests render either.
await Promise.all([loadMessages("en-US"), loadMessages("zh-CN")]);

afterEach(cleanup);
