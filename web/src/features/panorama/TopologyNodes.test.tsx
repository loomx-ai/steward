import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { NodeProps } from "@xyflow/react";
import { afterEach, expect, it, vi } from "vitest";
import type { TopologyResource } from "@/api/types";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import {
  BareResourceNode,
  ExpandedResourceGroupFrameNode,
  ExpandedStackFrameNode,
  ExpandedVSwitchStackFrameNode,
  ResourceGroupNode,
  ResourceStackNode,
  VSwitchStackNode,
  VSwitchFrameNode,
  type ExpandedResourceGroupFlowNode,
  type ExpandedStackFlowNode,
  type ExpandedVSwitchStackFlowNode,
  type ResourceGroupFlowNode,
  type ResourceFlowNode,
  type StackFlowNode,
  type VSwitchStackFlowNode,
  type VSwitchFlowNode,
} from "./TopologyNodes";

vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>();
  return {
    ...actual,
    Handle: () => null,
    Position: { Left: "left", Right: "right" },
  };
});

const cleanup = { selectable: false, potential_blockers: 0 };
const resource: TopologyResource = {
  key: "ecs-a",
  asset_id: "ecs-a",
  resource_kind_id: "ecs",
  name: "production-api",
  native_id: "i-production-api",
  type_name: "ECS Instance",
  icon: "/icons/ecs.svg",
  domain: "compute",
  finding_count: 0,
  actionable: false,
  cleanup,
};

afterEach(() => {
  localStorage.clear();
});

function nodeProps<NodeType>(
  id: string,
  data: Record<string, unknown>,
): NodeProps<
  NodeType &
    ResourceFlowNode &
    VSwitchFlowNode &
    StackFlowNode &
    ExpandedStackFlowNode &
    ResourceGroupFlowNode &
    ExpandedResourceGroupFlowNode &
    VSwitchStackFlowNode &
    ExpandedVSwitchStackFlowNode
> {
  return {
    id,
    data,
    type: "resource",
    selected: false,
    dragging: false,
    zIndex: 0,
    selectable: false,
    deletable: false,
    isConnectable: false,
    positionAbsoluteX: 0,
    positionAbsoluteY: 0,
  } as unknown as NodeProps<
    NodeType &
      ResourceFlowNode &
      VSwitchFlowNode &
      StackFlowNode &
      ExpandedStackFlowNode &
      ResourceGroupFlowNode &
      ExpandedResourceGroupFlowNode &
      VSwitchStackFlowNode &
      ExpandedVSwitchStackFlowNode
  >;
}

it("renders a bare resource icon with its name and cloud resource ID without a card surface", () => {
  const onActivate = vi.fn();
  const onContextMenu = vi.fn();
  const props = nodeProps<ResourceFlowNode>("ecs-a", {
    resource,
    selected: false,
    related: true,
    pendingCleanup: false,
    onActivate,
    onContextMenu,
  });
  const { container, rerender } = render(
    <LocaleProvider>
      <BareResourceNode {...props} />
    </LocaleProvider>,
  );

  const button = screen.getByRole("button", { name: /production-api/ });
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector("[data-resource-icon-mask]")).toHaveStyle({
    maskImage: "url(/icons/ecs.svg)",
    WebkitMaskImage: "url(/icons/ecs.svg)",
  });
  expect(screen.getByText("i-production-api")).toBeVisible();
  expect(screen.getByText("ECS Instance")).toBeVisible();
  expect(button).toHaveAttribute("data-related", "true");
  expect(button).toHaveAccessibleName("production-api · Directly related");
  expect(button).not.toHaveAccessibleDescription();
  expect(button).toHaveClass("rounded-lg");
  expect(button.className).not.toMatch(
    /\bbg-(?:background|card)\b|\bshadow(?:-\w+)?\b|\bborder\b/,
  );
  expect(container.querySelector("[data-resource-description]")).toBeNull();
  expect(container.textContent).not.toMatch(/\b1 resource\b/i);

  fireEvent.click(button);
  expect(onActivate).toHaveBeenCalledOnce();
  expect(onActivate).toHaveBeenLastCalledWith(resource, button);

  rerender(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("ecs-a", {
          resource: { ...resource, native_id: "" },
          selected: true,
          related: false,
          pendingCleanup: false,
          onActivate,
          onContextMenu,
        })}
      />
    </LocaleProvider>,
  );
  expect(screen.queryByText("i-production-api")).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: /production-api/ }),
  ).toHaveAttribute("aria-current", "true");
  expect(
    screen.getByRole("button", { name: /production-api/ }),
  ).toHaveAccessibleName("production-api · Current resource");
});

it("summarizes child resources and expands the parent group with keyboard-sized controls", async () => {
  const user = userEvent.setup();
  const onExpand = vi.fn();
  const onCollapse = vi.fn();
  const childKeys = ["router-a", "router-b", "router-c"];
  const childTypeSummaries = [
    {
      resourceKindID: "transit-router",
      typeName: "Transit Router",
      count: 3,
    },
  ];
  const baseData = {
    resource,
    childKeys,
    directChildCount: childKeys.length,
    childTypeSummaries,
    childFindingCount: 2,
    selected: true,
    related: false,
    pendingCleanup: false,
    cleanupTarget: null,
    interactionEnabled: true,
    onSelect: () => true,
    onActivate: vi.fn(),
    onContextMenu: vi.fn(),
  };
  const { container, rerender } = render(
    <LocaleProvider>
      <ResourceGroupNode
        {...nodeProps<ResourceGroupFlowNode>("ecs-a", {
          ...baseData,
          onExpand,
        })}
      />
    </LocaleProvider>,
  );

  const collapsedGroup = container.querySelector("[data-resource-group]");
  expect(collapsedGroup).toHaveAttribute("data-child-count", "3");
  expect(collapsedGroup).not.toHaveClass("overflow-hidden");
  expect(collapsedGroup).toHaveClass("grid-cols-[minmax(0,1fr)]");
  expect(
    container.querySelector("[data-selection-indicator]"),
  ).toBeInTheDocument();
  expect(screen.getByText(/3 child resources/)).toBeVisible();
  expect(screen.getByText(/2 findings/)).toBeVisible();
  const expand = screen.getByRole("button", {
    name: "Expand 3 child resources for production-api",
  });
  expect(expand).toHaveClass(
    "relative",
    "min-h-11",
    "w-full",
    "min-w-0",
    "max-w-full",
    "overflow-hidden",
    "pr-12",
  );
  expect(expand).toHaveClass("rounded-b-[7px]");
  expect(expand).toHaveAttribute("aria-expanded", "false");
  expect(expand.querySelector("[data-resource-group-toggle-icon]")).toHaveClass(
    "absolute",
    "right-3",
    "size-7",
    "pointer-events-none",
  );
  await user.click(expand);
  expect(onExpand).toHaveBeenCalledWith("ecs-a", expand);

  rerender(
    <LocaleProvider>
      <ExpandedResourceGroupFrameNode
        {...nodeProps<ExpandedResourceGroupFlowNode>("ecs-a", {
          ...baseData,
          onCollapse,
        })}
      />
    </LocaleProvider>,
  );
  const collapse = screen.getByRole("button", {
    name: "Collapse child resources for production-api",
  });
  const expandedGroup = container.querySelector(
    "[data-resource-group-expanded]",
  );
  const expandedHeader = container.querySelector("[data-resource-group-label]");
  expect(expandedGroup).toHaveClass(
    "rounded-lg",
    "border-l-2",
    "bg-muted/[0.12]",
  );
  expect(expandedGroup).not.toHaveClass("border", "shadow-sm");
  expect(expandedHeader).toHaveClass("h-10");
  expect(expandedHeader).not.toHaveClass("border-b");
  expect(screen.getByText("· 3")).toBeVisible();
  expect(collapse).toHaveClass("size-7");
  expect(collapse).toHaveAttribute("aria-expanded", "true");
  await user.click(collapse);
  expect(onCollapse).toHaveBeenCalledWith("ecs-a", childKeys, collapse);
});

it("shows a framed dirty-resource marker without hiding pending cleanup", () => {
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("ecs-a", {
          resource: { ...resource, dirty: true },
          selected: true,
          related: false,
          pendingCleanup: true,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("button", { name: /production-api/ }),
  ).toHaveAccessibleName(
    "production-api · Current resource · Pending cleanup · Dirty resource",
  );
  expect(container.querySelector("[data-dirty-asset-label]")).toHaveTextContent(
    "Dirty resource",
  );
  expect(
    container.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("Pending cleanup");
});

it("uses a neutral status background for an unselected dirty resource", () => {
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("ecs-a", {
          resource: { ...resource, dirty: true },
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  const outer = screen.getByRole("button", {
    name: /production-api/,
  }).parentElement;
  const dirtyLabel = container.querySelector("[data-dirty-asset-label]");
  expect(outer).toHaveAttribute("data-dirty-asset", "true");
  expect(outer).toHaveClass("rounded-lg", "bg-muted/50");
  expect(dirtyLabel).toHaveClass(
    "border-border",
    "bg-muted",
    "text-muted-foreground",
  );
  expect(dirtyLabel).not.toHaveClass("text-destructive");
  expect(dirtyLabel).toHaveTextContent("Dirty resource");
});

it("keeps resource context actions available while canvas modes disable activation", async () => {
  const user = userEvent.setup();
  const onActivate = vi.fn();
  const onContextMenu = vi.fn();
  const onViewDetails = vi.fn();
  render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("ecs-a", {
          resource,
          selected: false,
          related: false,
          pendingCleanup: false,
          interactionEnabled: false,
          onActivate,
          onContextMenu,
          contextMenu: {
            onViewDetails,
            onAddToCleanup: vi.fn(),
            onRemoveFromCleanup: vi.fn(),
            onOpenChange: vi.fn(),
          },
        })}
      />
    </LocaleProvider>,
  );

  const button = screen.getByRole("button", { name: /production-api/ });
  await user.click(button);
  expect(onActivate).not.toHaveBeenCalled();

  fireEvent.contextMenu(button);
  expect(onContextMenu).toHaveBeenCalledWith(resource, button);
  await user.click(
    await screen.findByRole("menuitem", { name: "View details" }),
  );
  expect(onViewDetails).toHaveBeenCalledOnce();
});

it.each([
  {
    name: "selected",
    selected: true,
    related: false,
    pendingCleanup: false,
    buttonClasses: ["ring-2", "ring-ring"],
    outerClasses: [] as string[],
    accessibleName: "production-api · Current resource",
  },
  {
    name: "related",
    selected: false,
    related: true,
    pendingCleanup: false,
    buttonClasses: ["ring-1", "ring-ring/55", "bg-ring/[0.03]"],
    outerClasses: [] as string[],
    accessibleName: "production-api · Directly related",
  },
  {
    name: "pending cleanup",
    selected: false,
    related: false,
    pendingCleanup: true,
    buttonClasses: [] as string[],
    outerClasses: ["bg-warning/5"],
    accessibleName: "production-api · Pending cleanup",
  },
  {
    name: "selected and pending cleanup",
    selected: true,
    related: false,
    pendingCleanup: true,
    buttonClasses: ["ring-2", "ring-ring"],
    outerClasses: [] as string[],
    accessibleName: "production-api · Current resource · Pending cleanup",
  },
  {
    name: "related and pending cleanup",
    selected: false,
    related: true,
    pendingCleanup: true,
    buttonClasses: ["ring-1", "ring-ring/55", "bg-ring/[0.03]"],
    outerClasses: ["bg-warning/5"],
    accessibleName: "production-api · Directly related · Pending cleanup",
  },
])(
  "renders independent positive visual states for a $name resource",
  ({
    selected,
    related,
    pendingCleanup,
    buttonClasses,
    outerClasses,
    accessibleName,
  }) => {
    const onContextMenu = vi.fn();
    const { container } = render(
      <LocaleProvider>
        <BareResourceNode
          {...nodeProps<ResourceFlowNode>("ecs-a", {
            resource,
            selected,
            related,
            pendingCleanup,
            onActivate: vi.fn(),
            onContextMenu,
          })}
        />
      </LocaleProvider>,
    );

    const button = screen.getByRole("button", { name: /production-api/ });
    const outer = button.parentElement;
    expect(button).toHaveAccessibleName(accessibleName);
    expect(button).not.toHaveAccessibleDescription();
    for (const className of buttonClasses) {
      expect(button).toHaveClass(className);
    }
    for (const className of outerClasses) {
      expect(outer).toHaveClass(className);
    }
    if (related) {
      expect(screen.getByText("Directly related")).toBeVisible();
    }
    if (pendingCleanup) {
      expect(outer).toHaveAttribute("data-cleanup-pending", "true");
      expect(
        container.querySelector("[data-pending-cleanup-label]"),
      ).toHaveTextContent("Pending cleanup");
    } else {
      expect(outer).not.toHaveAttribute("data-cleanup-pending");
      expect(
        container.querySelector("[data-pending-cleanup-label]"),
      ).toBeNull();
    }
    const selectionIndicator = container.querySelector(
      "[data-selection-indicator]",
    );
    if (selected) {
      expect(selectionIndicator).toBeVisible();
    } else {
      expect(selectionIndicator).toBeNull();
    }
    expect(button).not.toHaveClass("ring-warning");
    expect(outer).not.toHaveClass("ring-warning");
    fireEvent.contextMenu(button);
    expect(onContextMenu).toHaveBeenCalledOnce();
    expect(onContextMenu).toHaveBeenCalledWith(resource, button);
    expect(container.querySelector("[data-dimmed]")).toBeNull();
    expect(container.querySelector(".opacity-25")).toBeNull();
  },
);

it("places name and ID immediately after the icon and above the localized type", () => {
  localStorage.setItem("steward.locale", "zh-CN");
  const codedResource = {
    ...resource,
    resource_kind_id: "alicloud:ACS::VPC::RouteTable",
    type_name: "ACS::VPC::RouteTable",
    type_names: {
      "zh-CN": "VPC 路由表",
      "en-US": "VPC Route Table",
    },
    name: "生产路由表",
    native_id: "vtb-production",
  };
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("route-table", {
          resource: codedResource,
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  const identity = container.querySelector("[data-resource-identity]");
  const iconCell = container.querySelector("[data-resource-icon-cell]");
  const details = container.querySelector("[data-resource-details]");
  const typeLabel = container.querySelector("[data-resource-type]");
  expect(identity).toHaveTextContent("VPC 路由表");
  expect(identity).toContainElement(
    container.querySelector("[data-resource-icon-mask]"),
  );
  expect(details).toHaveTextContent("生产路由表");
  expect(details).toHaveTextContent("vtb-production");
  expect(details).toHaveClass("self-start", "pt-0.5");
  expect(typeLabel).toHaveTextContent("VPC 路由表");
  expect(typeLabel).toHaveAttribute("title", "VPC 路由表");
  expect(screen.queryByText("ACS::VPC::RouteTable")).not.toBeInTheDocument();
  expect(identity).toHaveAttribute("data-resource-layout");
  expect(Array.from(identity?.children ?? [])).toEqual([
    iconCell,
    details,
    typeLabel,
  ]);
});

it("does not repeat a resource ID when the name is already its unique identifier", () => {
  const uniqueNameResource = {
    ...resource,
    resource_kind_id: "alicloud:ACS::MessageService::Queue",
    type_name: "Queue",
    name: "ros-demo-event",
    native_id: "ros-demo-event",
  };
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("queue", {
          resource: uniqueNameResource,
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  const details = container.querySelector("[data-resource-details]");
  expect(details).toHaveTextContent("ros-demo-event");
  expect(details?.querySelector("small")).toBeNull();
  expect(details).toHaveClass("self-center");
});

it("centers the only identity value when either resource name or ID is missing", () => {
  render(
    <LocaleProvider>
      <>
        <BareResourceNode
          {...nodeProps<ResourceFlowNode>("name-only", {
            resource: {
              ...resource,
              key: "name-only",
              name: "release-to-oss-code",
              native_id: "",
            },
            selected: false,
            related: false,
            pendingCleanup: false,
            onActivate: vi.fn(),
            onContextMenu: vi.fn(),
          })}
        />
        <BareResourceNode
          {...nodeProps<ResourceFlowNode>("id-only", {
            resource: {
              ...resource,
              key: "id-only",
              name: "",
              native_id: "asg-6wedutffnymurnlc5tfl",
            },
            selected: false,
            related: false,
            pendingCleanup: false,
            onActivate: vi.fn(),
            onContextMenu: vi.fn(),
          })}
        />
      </>
    </LocaleProvider>,
  );

  for (const label of ["release-to-oss-code", "asg-6wedutffnymurnlc5tfl"]) {
    const button = screen.getByRole("button", { name: label });
    const details = button.querySelector("[data-resource-details]");
    expect(details).toHaveTextContent(label);
    expect(details?.querySelector("small")).toBeNull();
    expect(details).toHaveClass("self-center");
  }
});

it("describes the selected resource without marking ordinary resources related", () => {
  render(
    <LocaleProvider>
      <>
        <BareResourceNode
          {...nodeProps<ResourceFlowNode>("selected", {
            resource: { ...resource, key: "selected", name: "selected-node" },
            selected: true,
            related: false,
            pendingCleanup: false,
            onActivate: vi.fn(),
            onContextMenu: vi.fn(),
          })}
        />
        <BareResourceNode
          {...nodeProps<ResourceFlowNode>("ordinary", {
            resource: { ...resource, key: "ordinary", name: "ordinary-node" },
            selected: false,
            related: false,
            pendingCleanup: false,
            onActivate: vi.fn(),
            onContextMenu: vi.fn(),
          })}
        />
      </>
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("button", { name: /selected-node/ }),
  ).toHaveAccessibleName("selected-node · Current resource");
  expect(
    screen.getByRole("button", { name: "ordinary-node" }),
  ).not.toHaveAccessibleDescription();
});

it("opens network membership help without activating the resource", async () => {
  const user = userEvent.setup();
  const onActivate = vi.fn();
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("unknown-membership", {
          resource: {
            ...resource,
            key: "unknown-membership",
            name: "unplaced-eni",
            membership_unknown: true,
          },
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate,
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("button", { name: "unplaced-eni" }),
  ).toHaveAccessibleDescription("Network placement is unknown");

  const helpButton = screen.getByRole("button", {
    name: "Network placement is unknown",
  });
  expect(helpButton).toHaveClass("size-5", "rounded-full", "cursor-pointer");

  await user.click(helpButton);

  expect(
    container.ownerDocument.querySelector('[data-slot="popover-content"]'),
  ).toHaveTextContent("Network placement is unknown");
  expect(onActivate).not.toHaveBeenCalled();
});

it("does not show network membership help for a stack group", () => {
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("stack-group", {
          resource: {
            ...resource,
            key: "stack-group",
            name: "test-wordpress",
            class: "orchestration.stack_group",
            membership_unknown: true,
          },
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("button", { name: "test-wordpress" }),
  ).not.toHaveAccessibleDescription();
  expect(container.querySelector("[data-membership-unknown]")).toBeNull();
});

it("renders a compact two-line vSwitch identity and hides a zero resource count", () => {
  const { container, rerender } = render(
    <LocaleProvider>
      <VSwitchFrameNode
        {...nodeProps<VSwitchFlowNode>("vsw-a", {
          vSwitch: {
            key: "vsw-a",
            name: "application-zone",
            native_id: "vsw-a",
            zone: "cn-hangzhou-h",
            resource_count: 3,
            resource_keys: ["a", "b", "c"],
          },
        })}
      />
    </LocaleProvider>,
  );

  expect(screen.getByText("application-zone")).toBeVisible();
  expect(screen.getByText("vsw-a · cn-hangzhou-h")).toBeVisible();
  expect(screen.getByText("3 resources")).toBeVisible();
  expect(screen.getByRole("button", { name: /application-zone/ })).toHaveClass(
    "rounded-lg",
  );
  expect(container.querySelector("[data-vswitch-summary]")).not.toHaveClass(
    "border-b",
  );
  expect(container.querySelector("[data-vswitch-name]")).toHaveClass(
    "row-start-1",
    "truncate",
  );
  expect(container.querySelector("[data-vswitch-metadata]")).toHaveClass(
    "row-start-2",
    "truncate",
  );
  expect(container.querySelector("[data-resource-icon-mask]")).toHaveClass(
    "row-span-2",
    "self-center",
  );
  expect(container.querySelector("[data-vswitch-resource-count]")).toHaveClass(
    "row-start-1",
    "whitespace-nowrap",
  );

  rerender(
    <LocaleProvider>
      <VSwitchFrameNode
        {...nodeProps<VSwitchFlowNode>("vsw-empty", {
          vSwitch: {
            key: "vsw-empty",
            name: "empty-zone",
            native_id: "vsw-empty",
            zone: "cn-hangzhou-j",
            resource_count: 0,
            resource_keys: [],
          },
        })}
      />
    </LocaleProvider>,
  );

  expect(screen.queryByText("0 resources")).not.toBeInTheDocument();
  expect(
    container.querySelector("[data-vswitch-resource-count]"),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button")).toHaveAccessibleName(
    "empty-zone · vsw-empty · cn-hangzhou-j",
  );
});

it("renders provider icon tokens as local symbols instead of broken image URLs", () => {
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("ecs-a", {
          resource: { ...resource, icon: "shield" },
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector(".lucide-shield")).toBeInTheDocument();
});

it("renders ECS machine images as layers instead of photos", () => {
  const { container } = render(
    <LocaleProvider>
      <BareResourceNode
        {...nodeProps<ResourceFlowNode>("image-a", {
          resource: {
            ...resource,
            icon: "/icons/alicloud/acs-ecs-image.png",
          },
          selected: false,
          related: false,
          pendingCleanup: false,
          onActivate: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  expect(container.querySelector("[data-resource-icon-mask]")).toBeNull();
  expect(container.querySelector(".lucide-layers")).toBeInTheDocument();
});

it("reuses foreground-colored local icon masks for resource stacks", () => {
  const { container } = render(
    <LocaleProvider>
      <ResourceStackNode
        {...nodeProps<StackFlowNode>("stack:vsw-a:ecs", {
          stackKey: "stack:vsw-a:ecs",
          resourceKindID: "ecs",
          typeName: "ECS Instance",
          count: 3,
          icon: "/icons/alicloud/acs-ecs-instance.svg",
          pendingCleanup: false,
          pendingCleanupCount: 0,
          onExpand: vi.fn(),
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector("[data-resource-icon-mask]")).toHaveClass(
    "block",
    "bg-current",
    "text-foreground",
  );
  expect(container.querySelector("[data-resource-icon-mask]")).toHaveStyle({
    maskImage: "url(/icons/alicloud/acs-ecs-instance.svg)",
    WebkitMaskImage: "url(/icons/alicloud/acs-ecs-instance.svg)",
  });
});

it("labels partial and complete cleanup stacks while preserving their menu semantics", async () => {
  const user = userEvent.setup();
  const onContextMenu = vi.fn();
  const onAddToCleanup = vi.fn();
  const onRemoveFromCleanup = vi.fn();
  const { rerender } = render(
    <LocaleProvider>
      <ResourceStackNode
        {...nodeProps<StackFlowNode>("stack:vsw-a:ecs", {
          stackKey: "stack:vsw-a:ecs",
          resourceKindID: "ecs",
          typeName: "ECS Instance",
          count: 3,
          pendingCleanup: false,
          pendingCleanupCount: 1,
          onExpand: vi.fn(),
          onContextMenu,
          contextMenu: {
            onViewDetails: vi.fn(),
            onAddToCleanup,
            onRemoveFromCleanup,
            onOpenChange: vi.fn(),
          },
        })}
      />
    </LocaleProvider>,
  );

  const partial = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  expect(partial).toHaveAccessibleName(
    "ECS Instance · 3 resources · Pending cleanup 1/3",
  );
  expect(partial).not.toHaveAccessibleDescription();
  expect(partial).toHaveTextContent("Pending cleanup 1/3");
  expect(partial).not.toHaveAttribute("data-cleanup-pending");
  expect(partial).toHaveClass("rounded-lg", "bg-warning/5");
  expect(partial).not.toHaveClass("ring-warning");
  expect(
    partial.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("Pending cleanup 1/3");
  fireEvent.contextMenu(partial);
  expect(onContextMenu).toHaveBeenCalledWith("stack:vsw-a:ecs", partial);
  expect(
    await screen.findByRole("menuitem", {
      name: "Add all to resource list",
    }),
  ).toBeVisible();
  expect(
    screen.queryByRole("menuitem", {
      name: "Remove all from resource list",
    }),
  ).not.toBeInTheDocument();
  await user.keyboard("{Escape}");

  rerender(
    <LocaleProvider>
      <ResourceStackNode
        {...nodeProps<StackFlowNode>("stack:vsw-a:ecs", {
          stackKey: "stack:vsw-a:ecs",
          resourceKindID: "ecs",
          typeName: "ECS Instance",
          count: 3,
          pendingCleanup: true,
          pendingCleanupCount: 3,
          onExpand: vi.fn(),
          onContextMenu,
          contextMenu: {
            onViewDetails: vi.fn(),
            onAddToCleanup,
            onRemoveFromCleanup,
            onOpenChange: vi.fn(),
          },
        })}
      />
    </LocaleProvider>,
  );

  const complete = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  expect(complete).toHaveAccessibleName(
    "ECS Instance · 3 resources · Pending cleanup",
  );
  expect(complete).not.toHaveAccessibleDescription();
  expect(complete).toHaveAttribute("data-cleanup-pending", "true");
  expect(complete).toHaveClass("bg-warning/5");
  expect(complete).not.toHaveClass("ring-warning");
  expect(
    complete.querySelector("[data-pending-cleanup-label]"),
  ).toHaveTextContent("Pending cleanup");
  expect(screen.queryByText("Pending cleanup 3/3")).not.toBeInTheDocument();
  fireEvent.contextMenu(complete);
  expect(
    await screen.findByRole("menuitem", {
      name: "Remove all from resource list",
    }),
  ).toBeVisible();
  expect(
    screen.queryByRole("menuitem", {
      name: "Add all to resource list",
    }),
  ).not.toBeInTheDocument();
  expect(document.querySelector("[data-dimmed]")).toBeNull();
  expect(document.querySelector(".opacity-25")).toBeNull();
});

it("selects a stack from its body and expands it from the count or a double click", async () => {
  const user = userEvent.setup();
  const onExpand = vi.fn();
  const onSelect = vi.fn();
  const { rerender } = render(
    <LocaleProvider>
      <ResourceStackNode
        {...nodeProps<StackFlowNode>("stack:vsw-a:ecs", {
          stackKey: "stack:vsw-a:ecs",
          resourceKindID: "ecs",
          typeName: "ECS Instance",
          count: 3,
          icon: "shield",
          pendingCleanup: false,
          pendingCleanupCount: 0,
          onSelect,
          onExpand,
          onContextMenu: vi.fn(),
        })}
      />
    </LocaleProvider>,
  );

  const stack = screen.getByRole("button", {
    name: /ECS Instance.*3 resources/,
  });
  const expand = screen.getByRole("button", { name: "Expand ECS Instance" });
  const countBadge = expand.querySelector("[data-count-badge]");
  expect(expand).toHaveClass("top-3", "left-5", "size-7");
  expect(countBadge).toHaveClass("h-4", "min-w-4", "text-[9px]");
  expect(countBadge).toHaveTextContent("3");
  expect(stack.querySelector(".lucide-shield")).toBeInTheDocument();
  expect(stack.querySelectorAll(".lucide-boxes")).toHaveLength(0);
  stack.focus();
  await user.keyboard("{Enter}");
  expect(onSelect).toHaveBeenCalledOnce();
  expect(onExpand).not.toHaveBeenCalled();

  await user.click(expand);
  expect(onExpand).toHaveBeenCalledOnce();

  onExpand.mockClear();
  fireEvent.doubleClick(stack);
  expect(onExpand).toHaveBeenCalledOnce();

  const onCollapse = vi.fn();
  rerender(
    <LocaleProvider>
      <ExpandedStackFrameNode
        {...nodeProps<ExpandedStackFlowNode>("stack:vsw-a:ecs", {
          stackKey: "stack:vsw-a:ecs",
          resourceKindID: "ecs",
          typeName: "ECS Instance",
          count: 3,
          icon: "shield",
          onCollapse,
        })}
      />
    </LocaleProvider>,
  );
  const collapse = screen.getByRole("button", { name: "Collapse" });
  const group = collapse.closest("section");
  const label = collapse.closest("header");
  expect(group).toHaveAttribute("data-stack-group");
  expect(group).toHaveClass("rounded-lg", "border-l-2", "bg-muted/[0.12]");
  expect(group).not.toHaveClass("border");
  expect(label).toHaveAttribute("data-stack-group-label");
  expect(label).toHaveClass("h-10");
  expect(label).not.toHaveClass("border-b");
  expect(screen.getByText("· 3")).toBeVisible();
  expect(collapse).toHaveTextContent("");
  collapse.focus();
  await user.keyboard("{Enter}");
  expect(onCollapse).toHaveBeenCalledOnce();
});

it("disables expanded-stack collapse in box mode while retaining stack context actions", async () => {
  const user = userEvent.setup();
  const onCollapse = vi.fn();
  const onAddToCleanup = vi.fn();
  render(
    <LocaleProvider>
      <ExpandedStackFrameNode
        {...nodeProps<ExpandedStackFlowNode>("stack:vsw-a:ecs", {
          stackKey: "stack:vsw-a:ecs",
          resourceKindID: "ecs",
          typeName: "ECS Instance",
          count: 3,
          pendingCleanup: false,
          interactionEnabled: false,
          onCollapse,
          onContextMenu: vi.fn(),
          contextMenu: {
            onViewDetails: vi.fn(),
            onAddToCleanup,
            onRemoveFromCleanup: vi.fn(),
            onOpenChange: vi.fn(),
          },
        })}
      />
    </LocaleProvider>,
  );

  await user.click(screen.getByRole("button", { name: "Collapse" }));
  expect(onCollapse).not.toHaveBeenCalled();

  fireEvent.contextMenu(
    screen.getByRole("button", { name: "Collapse" }).closest("section")!,
  );
  await user.click(
    await screen.findByRole("menuitem", {
      name: "Add all to resource list",
    }),
  );
  expect(onAddToCleanup).toHaveBeenCalledOnce();
});

it("localizes a security-group stack and places the label below its icon", () => {
  localStorage.setItem("steward.locale", "zh-CN");
  const { container } = render(
    <LocaleProvider>
      <ResourceStackNode
        {...nodeProps<StackFlowNode>(
          "stack:region-public:alicloud:ACS::ECS::SecurityGroup",
          {
            stackKey: "stack:region-public:alicloud:ACS::ECS::SecurityGroup",
            resourceKindID: "alicloud:ACS::ECS::SecurityGroup",
            typeName: "ECS Security Group",
            typeNames: {
              "zh-CN": "安全组",
              "en-US": "Security Group",
            },
            count: 8,
            icon: "/icons/alicloud/acs-ecs-securitygroup.png",
            pendingCleanup: false,
            pendingCleanupCount: 1,
            onExpand: vi.fn(),
            onContextMenu: vi.fn(),
          },
        )}
      />
    </LocaleProvider>,
  );

  const stack = screen.getByRole("button", { name: /安全组.*8 个资源/ });
  expect(stack).toHaveClass("flex-col", "items-start");
  expect(screen.getByText("安全组")).toBeVisible();
  expect(screen.getByText("待清理 1/8")).toBeVisible();
  expect(screen.queryByText("Security Group")).not.toBeInTheDocument();
  expect(container.querySelector("[data-resource-icon-mask]")).toHaveStyle({
    maskImage: "url(/icons/alicloud/acs-ecs-securitygroup.png)",
  });
});

it("uses the localized catalog type for an aggregate without member details", () => {
  localStorage.setItem("steward.locale", "zh-CN");
  const { container } = render(
    <LocaleProvider>
      <ResourceStackNode
        {...nodeProps<StackFlowNode>(
          "stack:region-public:alicloud:ACS::VPC::RouteTable",
          {
            stackKey: "stack:region-public:alicloud:ACS::VPC::RouteTable",
            resourceKindID: "alicloud:ACS::VPC::RouteTable",
            typeName: "ACS::VPC::RouteTable",
            typeNames: {
              "zh-CN": "VPC 路由表",
              "en-US": "VPC Route Table",
            },
            count: 8,
            icon: "/icons/alicloud/acs-vpc-routetable.svg",
            pendingCleanup: false,
            pendingCleanupCount: 0,
            onExpand: vi.fn(),
            onContextMenu: vi.fn(),
          },
        )}
      />
    </LocaleProvider>,
  );

  expect(
    screen.getByRole("button", { name: /VPC 路由表.*8 个资源/ }),
  ).toBeVisible();
  expect(screen.queryByText("ACS::VPC::RouteTable")).not.toBeInTheDocument();
  expect(container.querySelector("[data-resource-details]")).toBeNull();
});

it("renders an empty vSwitch stack as one icon with a count and expands it", async () => {
  const user = userEvent.setup();
  const onExpand = vi.fn();
  const onCollapse = vi.fn();
  const onSelect = vi.fn();
  const vSwitches = [
    {
      key: "vsw-a",
      name: "batch-zone",
      native_id: "vsw-a",
      zone: "cn-hangzhou-j",
      resource_count: 0,
      resource_keys: [],
    },
    {
      key: "vsw-b",
      name: "cache-zone",
      native_id: "vsw-b",
      zone: "cn-hangzhou-k",
      resource_count: 0,
      resource_keys: [],
    },
  ];
  const cleanupTargets = vSwitches.map((vSwitch) => ({
    key: `asset:${vSwitch.key}`,
    kind: "resource" as const,
    connectionId: "connection-a",
    displayName: vSwitch.name,
    selector: { kind: "asset" as const, asset_id: vSwitch.key },
    ancestryKeys: [`asset:${vSwitch.key}`],
  }));
  const cleanupTargetsByVSwitchKey = new Map(
    vSwitches.map((vSwitch, index) => [vSwitch.key, cleanupTargets[index]!]),
  );
  const { rerender } = render(
    <LocaleProvider>
      <VSwitchStackNode
        {...nodeProps<VSwitchStackFlowNode>("vswitch-stack:vpc-a", {
          stackKey: "vswitch-stack:vpc-a",
          count: 2,
          vSwitches,
          selected: false,
          pendingCleanup: false,
          pendingCleanupCount: 0,
          cleanupTargets,
          interactionEnabled: true,
          onSelect,
          onExpand,
        })}
      />
    </LocaleProvider>,
  );

  const stack = screen.getByRole("button", { name: /Empty vSwitches.*2/ });
  expect(stack.tagName).toBe("BUTTON");
  expect(stack).toHaveClass("nodrag", "nopan", "rounded-lg");
  const expand = screen.getByRole("button", { name: "Expand Empty vSwitches" });
  const countBadge = expand.querySelector("[data-count-badge]");
  expect(expand).toHaveClass("top-3", "left-5", "size-7");
  expect(countBadge).toHaveClass("h-4", "min-w-4", "text-[9px]");
  expect(countBadge).toHaveTextContent("2");
  expect(stack).toHaveClass("flex-col", "items-start");
  expect(stack.querySelector(".lucide-network")).not.toBeInTheDocument();
  expect(stack.querySelector("[data-resource-icon-mask]")).toHaveStyle({
    maskImage: "url(/icons/alicloud/acs-vpc-vswitch.svg)",
    WebkitMaskImage: "url(/icons/alicloud/acs-vpc-vswitch.svg)",
  });
  expect(stack.querySelector("[data-vswitch-stack-frame]")).toBeNull();
  expect(stack.querySelector("[data-vswitch-stack-boundary]")).toBeNull();
  expect(screen.getByText("Empty vSwitches")).toBeVisible();
  expect(screen.queryByText("0 resources")).not.toBeInTheDocument();
  stack.focus();
  await user.keyboard(" ");
  expect(onExpand).not.toHaveBeenCalled();
  expect(onSelect).toHaveBeenCalledWith(cleanupTargets, false);
  await user.click(expand);
  expect(onExpand).toHaveBeenCalledOnce();

  rerender(
    <LocaleProvider>
      <ExpandedVSwitchStackFrameNode
        {...nodeProps<ExpandedVSwitchStackFlowNode>("vswitch-stack:vpc-a", {
          stackKey: "vswitch-stack:vpc-a",
          count: 2,
          vSwitches,
          cleanupTargets,
          cleanupTargetsByVSwitchKey,
          selectedTargetKeys: new Set(),
          pendingTargetKeys: new Set(),
          interactionEnabled: true,
          onSelect,
          onCollapse,
        })}
      />
    </LocaleProvider>,
  );
  expect(screen.getByText("batch-zone")).toBeVisible();
  expect(screen.getByText("cache-zone")).toBeVisible();
  expect(screen.getByText("vsw-a")).toBeVisible();
  expect(screen.getByText("vsw-b")).toBeVisible();
  expect(screen.queryByText("0 resources")).not.toBeInTheDocument();
  const collapse = screen.getByRole("button", { name: "Collapse" });
  const group = collapse.closest("section");
  const label = collapse.closest("header");
  expect(group).toHaveClass("rounded-lg", "border-l-2", "bg-muted/[0.12]");
  expect(group).not.toHaveClass("border", "bg-background/50");
  expect(label).toHaveClass("h-10", "px-3");
  expect(screen.getByText("· 2")).toBeVisible();
  expect(collapse).toHaveClass("size-7", "rounded-sm");
  expect(group?.querySelector("[data-vswitch-stack-grid]")).toHaveStyle({
    gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
    gridAutoRows: "72px",
  });
  expect(screen.getByRole("button", { name: /batch-zone/ })).toHaveClass(
    "rounded-lg",
    "border-border/70",
  );
  collapse.focus();
  await user.keyboard("{Enter}");
  expect(onCollapse).toHaveBeenCalledOnce();
});
