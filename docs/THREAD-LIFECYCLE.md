# T3 thread lifecycle

English | [简体中文](THREAD-LIFECYCLE.zh-CN.md)

## Naming and eligibility

The Bridge saves the title before launching a new idle thread and submits it with the original stable `clientRequestId`. It derives ordinary titles from the user's first visible input after normalization and removal of the bot mention. For a merged message, it uses the text from the actual input parts. An attachment-only message uses the attachment title. Quotes, machine context, resource keys, and hidden logs are excluded. Titles use standard display redaction and omit URLs and private-key markers. The ordinary title is limited to 60 characters. Titles for verified events use the node, check, and start time, with a purpose label and Shanghai time to the minute. The total limit is 80 characters. Later turns keep the original title.

Lifecycle checks consider tasks from the private Store. They verify the task ID derived from its source, receiving app binding, current target policy, and user ACL, then use preflight and thread readback to check the environment, project, driver, provider, model, options, and access mode. Eligible threads are native top-level MCP threads. The most recent run must match a recorded original run or a verified delegated completion. Later independent runs, archived threads, or missing evidence block organization. Newly launched threads have no follow-up rename. Legacy migration renames only the exact old generated placeholder and preserves manually edited titles.

When waiting for delegated work, T3 may return the most recent result run in the thread. The Bridge checks consecutive completion notices after the original run against the initiating run's delegation names, counts, and distinct task IDs. It can exclude previously delivered historical results and accepts reused names in later turns. Runs with independent input are excluded. If a newer independent turn already exists, retrieving the earlier result requires verification of every attributed delegation. The Bridge records that result run ID separately and keeps the original run ID in the message and execution receipts. Ambiguous ownership, truncated content, ambiguous delimiters, or missing evidence leave the task pending reconciliation. [T3 result selection](https://github.com/pingdotgg/t3code/blob/7d32075e3757ded707914e2450c3e392a9665765/packages/provider-core/src/server/subagentProjection.ts#L288-L329) · [T3 completion notices](https://github.com/pingdotgg/t3code/blob/7d32075e3757ded707914e2450c3e392a9665765/packages/provider-core/src/server/notification.ts#L172-L252)

The Bridge can automatically settle one-off automation, event, and review tasks, fully reconciled stopped or failed tasks, sessions left behind by `/new` or `/reset`, and a previous project session after a successful switch. The current binding includes user, chat, topic, route, target, and project. A project change marks only the previous task as detached. Current ordinary sessions stay open. Older state without a confirmed binding is left open as well. `/resume` or explicit continuation clears detachment and preserves the original thread and turns.

Settlement requires all of the conditions below.

- The task is complete, failed, or stopped and its final card receipt is confirmed. Pending notifications, unsubmitted inputs, queued follow-ups, open forms, outstanding work, and blocked or clarification states prevent settling.
- A task requiring reconciliation or with an unconfirmed stop waits. A stop acknowledgment alone does not establish that native work is finished.
- The latest and recent native runs are terminal, `activeRunId` is explicitly null, and there are no pending approvals, questions, background work, or thread waits. Known child threads must also be terminal and have no active or pending work. An idle thread counts as quiet only for a recorded terminal run.
- After the T3 readback, the Bridge checks local notifications, stops, and queues again just before calling organize. T3 handles concurrent writes by other clients according to its own semantics.

## Receipts, continuation, and failure

Thread metadata records title state, the settled receipt for each request, detachment, attempt and next-check times, and a fixed low-sensitivity issue code. Updates preserve the source, thread, run, request, result, stop, baseline, and card hash. An already-settled native thread supplies a receipt. A confirmed request respects a subsequent manual unsettle. A new request can settle independently.

The background pass runs on the two-second poll, checks up to two tasks, and has a ten-second total metadata budget. The 100 ms merged-input wake-up only handles input. Failed metadata attempts are limited to three per request and spaced by at least 30 seconds. A known busy task is checked again after 30 seconds. Restart reads back state before any metadata change. It does not replay a business send or stop. Receipt checks use bounded state under the lock.

Before continuing a settled thread, the Bridge gives the metadata operation an independent two-second context, unsets settlement, and reads it back. Continuation reuses the thread and does not automatically revive stopped or rejected tasks. Optional metadata errors are visible in the requester's `/status`. The actual send still uses its durable request guard. If T3 rejects the send or its acknowledgment is unknown, the Bridge reads back state and leaves the request pending reconciliation.

## T3 API

- `t3_thread_launch` supplies the saved title alongside the existing request ID and controlled execution identity.
- `t3_thread_update(action=rename)` uses a stable `title-v1` request, checks the acknowledgment, and reads back the title.
- `t3_thread_organize(action=settle|unsettle)` records the attempt before sending and reads back after an unknown acknowledgment.
- The Bridge reuses `t3_thread_read`, read-only `t3_thread_configure`, and existing environment, project, and capability preflight calls.
