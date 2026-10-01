---
title: "Scheduled scans"
description: "Scan connections automatically on a schedule, decide how skipped, missed and failing runs are handled, follow each run and data freshness, and get notified when scans fail."
navTitle: "Scheduled scans"
---

Scheduled scans keep the inventory current without someone remembering to click **Start scan**. Each run is an ordinary scan: its results, logs and [resource changes](./scans.md#changes) look the same as a manual scan's.

Scheduled scans are off by default. Add one for the active connection under **Scans** → **Scheduled scans**.

<span id="create"></span>

## Add a scheduled scan

1. Check the **Active connection** at the top left. The schedule belongs to that connection.
2. Open **Scans** → **Scheduled scans** → **New scheduled scan**.
3. Enter a **Name**, then choose the **Scan scope** and **Resource kind**. The scope options are the same as for [starting a scan](./scans.md#scope).
4. Choose a **Frequency** and **Time zone**, and check **Next 3 runs**.
5. Click **Save**. To check permissions and results first, click **Save and run now**.

A connection can have several schedules, for example a full scan once a day and a core-network check every six hours.

| Frequency | Runs |
| --- | --- |
| **Every few hours** | Every 1, 2, 3, 4, 6, 8 or 12 hours from the **First run of the day**, at the same times every day. |
| **Daily** | Once a day at the chosen time. |
| **Weekly** | On the chosen days at the chosen time. |
| **Monthly** | On one day from 1 to 28 at the chosen time. |
| **Cron** | A five-field cron expression: minute hour day-of-month month day-of-week. |

Times follow the chosen **Time zone**, including its daylight-saving changes. Runs must be at least one hour apart, and at least six hours apart on [Steward Cloud](https://steward.console.loomx.ai).

**All active regions + global** is resolved at each run, so regions you enable later are included automatically.

<span id="rules"></span>

## Run rules

**Run rules** are collapsed by default; their summary shows in the heading.

| Situation | Choices | Default |
| --- | --- | --- |
| The previous scan is still running: the connection still has a scan in progress | **Skip this time**, or **Run when it finishes** | Skip |
| Steward was not running at the planned time | **Run once when it is back**, or **Wait for the next time** | Run once |
| Some targets fail | **Retry them once**, or **Do not retry** | Retry once |
| Whole scans keep failing | Pause after 2, 3 or 5 in a row | 3 |

- However many times were missed, Steward catches up at most once.
- A paused manual scan does not count as in progress and does not hold up schedules.
- A whole scan fails when every target fails or the scan cannot start, usually because a credential expired or access was revoked. Scans where only some targets fail do not count.

<span id="runs"></span>

## Follow the runs

Click a schedule's name to open it. **Runs** lists every planned time, including times that did not start a scan:

| Result | Meaning |
| --- | --- |
| **Succeeded**, **Failed** | The scan's final status. |
| **N failed** | Some targets failed. The first one's region, resource kind and reason show underneath, with whether it was retried automatically. |
| **Skipped** | The connection was still scanning, or Steward was not running and the rule says not to catch up. |
| **Could not start** | The scan was not created, for example because the connection has not passed validation. |

**Caught up** under a planned time marks a run made after Steward came back from a missed time. **Run now** marks a run someone started by hand; it does not move the next planned time.

The **Changes** column shows `+added −removed ~modified`; click it for the details. In **Scans** → **Scan tasks**, scans started by a schedule show the schedule's name under **Requested by**; use **Source** to show only manual or scheduled scans.

<span id="pause"></span>

## Automatic pause and resume

When whole scans fail too many times in a row, Steward pauses the schedule so it stops calling the cloud APIs. Then:

- The schedule list, the schedule page and the top of other pages show the pause and the last failure.
- In the connection switcher at the top left, the connection shows a red **Paused**.
- Once you replace the credential under **Settings** → **Cloud connections** and it passes validation, schedules paused for failures resume on their own from the next planned time. Times passed while paused are not caught up.

You can also **Pause** and **Resume** a schedule yourself. Steward never resumes a schedule a person paused.

<span id="freshness"></span>

## Data freshness

**Resource Panorama**, **Resources**, **Cleanup** and **Findings** show, at the top right, when the active connection last had a **complete scan** and when the next run is. A complete scan covers all active regions plus global resources and every resource kind, and succeeds without failed targets; manual scans count too. Scans of selected regions, networks or resource kinds do not refresh this time.

The dot next to each connection in the connection switcher means:

| Dot | Meaning |
| --- | --- |
| Green | Scheduled scans are on and a complete scan finished in the last 24 hours |
| Yellow | Scheduled scans are on but the last complete scan is older than 24 hours |
| Red | A schedule was paused automatically |
| Grey | No schedule is on |

Deleting keys and identities requires a complete scan. When that blocks a cleanup task, the task shows the last complete scan and a **Start a complete scan** button. See [Clean up resources](./cleanup.md#review).

<span id="settings"></span>

## Workspace settings

Admins manage these under **Settings** → **Scheduled scans**:

- **Default for new connections**: when on, a connection's first successful validation creates a "Daily full scan" of all active regions plus global, at a random time between 01:00 and 06:00 so connections do not all scan at once. It is off by default and does not touch existing connections.
- **Default time zone**: the time zone new schedules start in.
- **Keep scheduled scans for**: 7, 30 or 90 days, or forever; 30 days by default. Only scans started by schedules, with their logs and changes, are removed. Manual scans are not affected, and each schedule's latest successful scan is always kept.
- **All scheduled scans**: every connection's schedules, state, next run and last complete scan. Click a connection to switch to it and open its schedules.

<span id="notifications"></span>

## Failure notifications

Admins add channels under **Settings** → **Notifications** to hear about scheduled scans that fail, partly fail or pause themselves.

| Type | What you enter |
| --- | --- |
| **Feishu**, **DingTalk** | The robot's webhook address, plus the **Signing secret** if signature checks are on. If keyword checks are on, use `Steward` as the keyword. |
| **WeCom**, **Slack** | The robot or incoming-webhook address. |
| **Webhook** | Any address that accepts JSON. The body carries `event`, `schedule`, `connection`, `scan_task_id`, `detail` and a readable `text`. |

- Each channel chooses its events and message language. Click **Send test** to check delivery; the last result shows under the channel.
- Webhook addresses and signing secrets are encrypted with the same key as cloud credentials. The console shows only their last characters.
- A failed delivery is retried twice and never affects the scan.
- With `STEWARD_PUBLIC_URL` set, messages link to the scan. See the [configuration reference](./configuration.md).
- On Steward Cloud, channels can only send to public addresses.

<aside class="docs-note">Scheduled scans run while the Steward server runs. On a laptop, nothing scans while it sleeps or Steward is stopped; when it is back, the <strong>Steward was not running</strong> rule applies. For dependable schedules, <a href="./deployment.md">deploy Steward on a server</a>.</aside>
