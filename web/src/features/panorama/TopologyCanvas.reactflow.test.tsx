import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { TopologyResource, VPCTopologyView } from "@/api/types";
import { ThemeProvider } from "@/app/ThemeProvider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { TopologyCanvas } from "./TopologyCanvas";

vi.mock("./CleanupSelectionContext", () => ({
  useCleanupSelection: () => ({
    targets: [],
    addTargets: vi.fn(),
    removeTarget: vi.fn(),
    removeBatchMember: vi.fn(),
  }),
}));

vi.mock("@/connections/ActiveConnectionProvider", () => ({
  useRequiredConnection: () => ({ id: "connection-a" }),
}));

const cleanup = { selectable: false, potential_blockers: 0 };

function resource(
  key: string,
  name: string,
  resourceKindID: string,
): TopologyResource {
  return {
    key,
    asset_id: key,
    resource_kind_id: resourceKindID,
    name,
    native_id: `${key}-native-id`,
    type_name: resourceKindID === "ecs" ? "ECS Instance" : "Security Group",
    domain: resourceKindID === "ecs" ? "compute" : "network",
    finding_count: 0,
    actionable: false,
    cleanup,
  };
}

const view: VPCTopologyView = {
  kind: "vpc",
  region: { key: "region-a", name: "杭州", native_id: "cn-hangzhou" },
  vpc: { key: "vpc-a", name: "production-vpc", native_id: "vpc-a" },
  public_resource_keys: [],
  vswitches: [
    {
      key: "vsw-a",
      asset_id: "asset-vsw-a",
      name: "application-zone",
      native_id: "vsw-a",
      resource_count: 4,
      resource_keys: ["sg", "ecs-a", "ecs-b", "ecs-c"],
    },
  ],
  resources: [
    resource("sg", "shared-security-group", "security-group"),
    resource("ecs-a", "isolated-a", "ecs"),
    resource("ecs-b", "isolated-b", "ecs"),
    resource("ecs-c", "isolated-c", "ecs"),
  ],
  edges: [],
};

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockReturnValue({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }),
  );
  vi.stubGlobal(
    "ResizeObserver",
    class ResizeObserver {
      readonly callback: ResizeObserverCallback;

      constructor(callback: ResizeObserverCallback) {
        this.callback = callback;
      }

      observe(target: Element) {
        this.callback(
          [{ target } as ResizeObserverEntry],
          this as unknown as globalThis.ResizeObserver,
        );
      }

      unobserve() {}
      disconnect() {}
    },
  );
  vi.stubGlobal(
    "DOMMatrixReadOnly",
    class DOMMatrixReadOnly {
      readonly m22 = 1;
    },
  );
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
    function getBoundingClientRect(this: Element) {
      const element = this as HTMLElement;
      const width = Number.parseFloat(element.style.width) || 1200;
      const height = Number.parseFloat(element.style.height) || 800;
      return {
        bottom: height,
        height,
        left: 0,
        right: width,
        top: 0,
        width,
        x: 0,
        y: 0,
        toJSON: () => undefined,
      };
    },
  );
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function revealMeasuredFlowNodes() {
  await waitFor(() =>
    expect(document.querySelector(".react-flow__node")).not.toBeNull(),
  );
  document
    .querySelectorAll<HTMLElement>(".react-flow__node")
    .forEach((node) => {
      node.style.visibility = "visible";
    });
}

it("omits the topology overview from the canvas", async () => {
  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <div style={{ width: 1200, height: 800 }}>
            <TopologyCanvas
              view={view}
              complete
              onSelectResource={vi.fn()}
              onClearFocus={vi.fn()}
            />
          </div>
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  await revealMeasuredFlowNodes();
  const flowNodes = document.querySelectorAll(".react-flow__node");
  expect(flowNodes.length).toBeGreaterThan(0);
  expect(
    screen.queryByRole("img", { name: "Topology overview" }),
  ).not.toBeInTheDocument();
});

it("keeps real React Flow wrappers pointer-interactive without duplicate node activation", async () => {
  const onSelectResource = vi.fn();
  const onClearFocus = vi.fn();
  const user = userEvent.setup();
  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <div style={{ width: 1200, height: 800 }}>
            <TopologyCanvas
              view={view}
              complete
              onSelectResource={onSelectResource}
              onClearFocus={onClearFocus}
            />
          </div>
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  await revealMeasuredFlowNodes();
  const resourceButton = screen.getByRole("button", {
    name: "shared-security-group",
  });
  expect(resourceButton.closest(".react-flow__node")).toHaveStyle({
    pointerEvents: "all",
  });
  await user.click(resourceButton);
  expect(onSelectResource).toHaveBeenCalledOnce();
  expect(onClearFocus).not.toHaveBeenCalled();

  fireEvent.click(
    screen.getByRole("button", {
      name: /application-zone.*vsw-a/,
    }),
  );
  expect(onClearFocus).toHaveBeenCalledOnce();
  onClearFocus.mockClear();

  const stackButton = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  await user.click(stackButton);
  expect(onClearFocus).toHaveBeenCalledOnce();
  expect(
    screen.getByRole("button", {
      name: /ECS Instance.*3 resources/,
    }),
  ).toBeVisible();
  onClearFocus.mockClear();
  await user.click(screen.getByRole("button", { name: "Expand ECS Instance" }));
  expect(onClearFocus).not.toHaveBeenCalled();
  await revealMeasuredFlowNodes();
  expect(
    screen.queryByRole("button", {
      name: /ECS Instance.*3 resources/,
    }),
  ).not.toBeInTheDocument();

  const collapseButton = screen.getByRole("button", { name: "Collapse" });
  fireEvent.click(collapseButton.closest("section") as HTMLElement);
  expect(onClearFocus).toHaveBeenCalledOnce();
  onClearFocus.mockClear();

  await user.click(collapseButton);
  expect(onClearFocus).not.toHaveBeenCalled();
  await revealMeasuredFlowNodes();
  expect(
    screen.getByRole("button", {
      name: /ECS Instance.*3 resources/,
    }),
  ).toBeVisible();
});

it("keeps real React Flow nodes active only in arrow selection mode", async () => {
  const onSelectResource = vi.fn();
  const user = userEvent.setup();
  render(
    <ThemeProvider>
      <TooltipProvider>
        <LocaleProvider>
          <div style={{ width: 1200, height: 800 }}>
            <TopologyCanvas
              view={view}
              complete
              onSelectResource={onSelectResource}
              onClearFocus={vi.fn()}
            />
          </div>
        </LocaleProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );

  await revealMeasuredFlowNodes();
  const resource = screen.getByRole("button", {
    name: "shared-security-group",
  });
  expect(screen.getByRole("button", { name: "Select" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(screen.getByRole("button", { name: "Pan" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  await user.click(resource);
  expect(onSelectResource).toHaveBeenCalledOnce();
  expect(resource).toHaveAttribute("aria-pressed", "true");

  await user.click(resource);
  expect(onSelectResource).toHaveBeenCalledOnce();
  expect(resource).toHaveAttribute("aria-pressed", "false");
});
