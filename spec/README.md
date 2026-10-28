# Specs

This directory holds the specs for the components in this repo, one file per component under `components/`.

## The spec is the source of truth

**The spec always defines how a component must behave.** Code, tests and other docs follow the spec, not the other way around.

- **Change the spec first.** Any change to a component's behavior starts with a spec change, reviewed together with (or before) the code.
- **Code that disagrees with the spec is a bug.** Fix the code, or, if the spec is wrong, fix the spec in the same PR.
- **Keep them in sync.** A PR that changes behavior without updating the spec is not complete.

## What a spec covers

Each component spec describes what users and other components can rely on:

1. **What it is**: the component's purpose.
2. **How to run it**: flags, environment variables, configuration.
3. **Input**: the resources or config it consumes.
4. **Output**: status, conditions, metrics, and anything else it produces.
5. **Behavior**: the rules that turn input into output, including edge cases.
6. **E2E**: the end-to-end cases that verify the behavior.

A spec describes behavior, not implementation. Leave out internal details that callers cannot observe or rely on.

## Components

| Spec | Component |
|------|-----------|
| [cluster-health-monitor.md](components/cluster-health-monitor.md) | Overlay monitor that runs control-plane checkers and exposes Prometheus metrics. |
| [checknodehealth-controller.md](components/checknodehealth-controller.md) | Controller that runs on-demand node health checks through `CheckNodeHealth` resources. |
| [node-controller.md](components/node-controller.md) | Controller that creates `CheckNodeHealth` resources for new and rebooted nodes and cleans up stale `NodeHealthy` conditions. |
