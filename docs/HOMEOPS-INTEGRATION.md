# HomeOps integration

English | [简体中文](HOMEOPS-INTEGRATION.zh-CN.md)

HomeOps is a separate operations system for self-hosted infrastructure. It runs health checks, tracks incidents and recovery, coordinates maintenance, and produces reports. HomeOps supplies operations data and coordination status. T3 Bridge verifies events and connects them to authorized Feishu conversations and T3 investigations.

Ordinary chat only needs the [single-route configuration](../config/generic.example.json). To enable the integration, start from the [HomeOps configuration example](../config/homeops.example.json) and supply your own checks, coordination command, signing source, and access rules.

- **Maintenance.** A route with `checks` needs an absolute `coordinationCommand` path. The Bridge reads the coordination state and applies the route's checks before dispatch or production writes. The T3 execution environment must enforce its own registration and write permissions.
- **Events.** Each `health` entry pins the source, expected node, local address, and private HMAC key file. The Bridge verifies `X-HomeOps-Signature` before accepting `homeops.eventbridge/v1` events. Set `eventChats`, `eventRoutes`, and route ACLs to limit which conversations can use an event. Alert investigation starts with read-only context.
- **Automatic investigation.** `autoInvestigations.enabled` defaults to `false`. Before replacing an existing dispatcher, stop it, drain tasks and reports, and run `-prepare-auto-baseline` to prepare the initial state. Events preceding that baseline retain their deduplication identities.
- **Reports.** `-submit-report` accepts a report from stdin and associates it with a task, request, and event. Configure report sources, output paths, and cross-host copies explicitly. A submitted report, recovery signal, and completed T3 run are tracked independently.

Before replacing an existing receiver, test the account, route identity, state format, outstanding tasks, and rollback procedure. [Start and deploy](RUNBOOK.md#start-and-deploy)
